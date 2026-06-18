package turboquant

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"runtime"
	"sync"
)

// Default configurations for the TurboQuant vector quantization
const (
	DefaultDimension = 1536
	DefaultBitWidth  = 4
	DefaultSeed      = 42
)

// Slice pools for reducing GC pressure in hot quantization/dequantization paths.
// Pools are keyed by slice length (dimension) to avoid capacity mismatches.
//
// IMPORTANT: Only use these pools for temporary intermediate slices that are
// NOT returned to the caller. Slices like QuantizedVector.Indices or the
// final dequantized float32 result must NOT come from the pool.

var (
	float64SlicePool sync.Map // map[int]*sync.Pool — pools keyed by dimension
)

// getFloat64Pool returns the sync.Pool for []float64 slices of the given dimension.
func getFloat64Pool(dim int) *sync.Pool {
	if v, ok := float64SlicePool.Load(dim); ok {
		return v.(*sync.Pool)
	}
	pool := &sync.Pool{
		New: func() any {
			s := make([]float64, dim)
			return &s
		},
	}
	actual, _ := float64SlicePool.LoadOrStore(dim, pool)
	return actual.(*sync.Pool)
}

// getFloat64Slice retrieves a []float64 slice of the given dimension from the pool.
// The returned slice is zeroed before return.
func getFloat64Slice(dim int) []float64 {
	pool := getFloat64Pool(dim)
	sp := pool.Get().(*[]float64)
	s := *sp
	// Zero the slice to prevent stale data leaking between uses.
	clear(s)
	return s
}

// putFloat64Slice returns a []float64 slice to the pool.
// Only slices whose length matches the pool dimension should be returned.
func putFloat64Slice(s []float64) {
	if len(s) == 0 {
		return
	}
	pool := getFloat64Pool(len(s))
	pool.Put(&s)
}

// TurboQuant is the core entry point of the SDK, encapsulating all quantization functionality.
type TurboQuant struct {
	dimension   int
	bitWidth    int
	codebook    *Codebook
	rotation    *Matrix
	concurrency int // max concurrent goroutines for batch operations
}

// options holds configurable parameters for NewTurboQuant.
type options struct {
	gridPoints  int
	iterations  int
	concurrency int // max concurrent goroutines for batch operations; 0 means runtime.NumCPU()
}

// defaultOptions returns the default codebook builder parameters.
func defaultOptions() options {
	return options{
		gridPoints:  50000,
		iterations:  300,
		concurrency: 0, // resolved to runtime.NumCPU() at construction time
	}
}

// Option is a functional option for configuring NewTurboQuant.
type Option func(*options)

// WithGridPoints sets the number of grid points for numerical integration
// in the Lloyd-Max codebook builder. Default is 50000.
func WithGridPoints(n int) Option {
	return func(o *options) {
		o.gridPoints = n
	}
}

// WithIterations sets the number of Lloyd-Max iterations for codebook
// construction. Default is 300.
func WithIterations(n int) Option {
	return func(o *options) {
		o.iterations = n
	}
}

// WithConcurrency sets the maximum number of concurrent goroutines used by
// QuantizeBatch and DequantizeBatch. The default (0) resolves to
// runtime.NumCPU(). Values less than 1 are treated as runtime.NumCPU().
func WithConcurrency(n int) Option {
	return func(o *options) {
		o.concurrency = n
	}
}

// NewTurboQuant creates and initializes a quantizer instance.
// dimension: vector dimension, must be >= 2
// bitWidth: quantization bit width, must be 2, 3, or 4
// seed: random seed for rotation matrix generation; same seed produces same matrix
// opts: optional functional options (WithGridPoints, WithIterations, WithConcurrency)
func NewTurboQuant(dimension, bitWidth int, seed int64, opts ...Option) (*TurboQuant, error) {
	if err := ValidateDimension(dimension); err != nil {
		return nil, fmt.Errorf("NewTurboQuant: %w", err)
	}
	if err := ValidateBitWidth(bitWidth); err != nil {
		return nil, fmt.Errorf("NewTurboQuant: %w", err)
	}

	defaults := defaultOptions()
	for _, opt := range opts {
		opt(&defaults)
	}

	var codebook *Codebook
	var err error

	// Use the global cache only when default parameters are used;
	// custom grid/iteration settings require a fresh build.
	if defaults.gridPoints == 50000 && defaults.iterations == 300 {
		codebook, err = GetOrBuildCodebook(dimension, bitWidth)
	} else {
		builder := &CodebookBuilder{
			gridPoints: defaults.gridPoints,
			iterations: defaults.iterations,
		}
		codebook, err = builder.Build(dimension, bitWidth)
	}
	if err != nil {
		return nil, fmt.Errorf("NewTurboQuant: failed to build codebook: %w", err)
	}

	rotation, err := NewRandomOrthogonalMatrix(dimension, seed)
	if err != nil {
		return nil, fmt.Errorf("NewTurboQuant: failed to generate rotation matrix: %w", err)
	}

	return &TurboQuant{
		dimension:   dimension,
		bitWidth:    bitWidth,
		codebook:    codebook,
		rotation:    rotation,
		concurrency: resolveConcurrency(defaults.concurrency),
	}, nil
}

// resolveConcurrency returns n if n >= 1, otherwise runtime.NumCPU().
func resolveConcurrency(n int) int {
	if n >= 1 {
		return n
	}
	return runtime.NumCPU()
}

// Concurrency returns the maximum number of concurrent goroutines used by
// batch operations.
func (tq *TurboQuant) Concurrency() int {
	return tq.concurrency
}

// Quantize quantizes a single float32 vector into a QuantizedVector.
func (tq *TurboQuant) Quantize(vec []float32) (*QuantizedVector, error) {
	return quantizeVector(vec, tq.rotation, tq.codebook)
}

// Dequantize reconstructs a float32 vector from a QuantizedVector.
func (tq *TurboQuant) Dequantize(qv *QuantizedVector) ([]float32, error) {
	return dequantizeVector(qv, tq.rotation, tq.codebook)
}

// Serialize serializes a QuantizedVector into a compact binary byte slice.
func (tq *TurboQuant) Serialize(qv *QuantizedVector) ([]byte, error) {
	return SerializeQuantizedVector(qv, tq.bitWidth)
}

// Deserialize deserializes a binary byte slice into a QuantizedVector.
func (tq *TurboQuant) Deserialize(data []byte) (*QuantizedVector, error) {
	return DeserializeQuantizedVector(data, tq.bitWidth, tq.dimension)
}

// CompressionRatio returns the theoretical compression ratio for the current configuration.
func (tq *TurboQuant) CompressionRatio() float64 {
	return CompressionRatio(tq.dimension, tq.bitWidth)
}

// Dimension returns the vector dimension of this quantizer.
func (tq *TurboQuant) Dimension() int {
	return tq.dimension
}

// BitWidth returns the quantization bit width of this quantizer.
func (tq *TurboQuant) BitWidth() int {
	return tq.bitWidth
}

// QuantizeBatch quantizes multiple vectors concurrently using a worker pool.
// Concurrency is controlled by the WithConcurrency option (default: runtime.NumCPU()).
// All vectors must have the same dimension as the TurboQuant instance.
// If any vector has a mismatched dimension, returns an error indicating the first such index.
func (tq *TurboQuant) QuantizeBatch(vecs [][]float32) ([]*QuantizedVector, error) {
	if len(vecs) == 0 {
		return nil, nil
	}

	// Check all vector dimensions first, returning the first mismatch index.
	for i, vec := range vecs {
		if len(vec) != tq.dimension {
			return nil, fmt.Errorf("QuantizeBatch: vector at index %d has dimension %d, expected %d", i, len(vec), tq.dimension)
		}
	}

	results := make([]*QuantizedVector, len(vecs))
	errs := make([]error, len(vecs))

	var wg sync.WaitGroup
	sem := make(chan struct{}, tq.concurrency)

	for i, vec := range vecs {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, v []float32) {
			defer func() { <-sem; wg.Done() }()
			qv, err := quantizeVector(v, tq.rotation, tq.codebook)
			if err != nil {
				errs[idx] = err
				return
			}
			results[idx] = qv
		}(i, vec)
	}

	wg.Wait()

	// Return the first error encountered.
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}

	return results, nil
}

// QuantizeFloat64 quantizes a single float64 vector into a QuantizedVector.
// It converts the input to float32 using Float64sToFloat32s, then delegates to Quantize.
func (tq *TurboQuant) QuantizeFloat64(vec []float64) (*QuantizedVector, error) {
	return tq.Quantize(Float64sToFloat32s(vec))
}

// DequantizeFloat64 reconstructs a float64 vector from a QuantizedVector.
// It delegates to Dequantize, then converts the result to float64 using Float32sToFloat64s.
func (tq *TurboQuant) DequantizeFloat64(qv *QuantizedVector) ([]float64, error) {
	f32, err := tq.Dequantize(qv)
	if err != nil {
		return nil, err
	}
	return Float32sToFloat64s(f32), nil
}

// QuantizeBatchFloat64 batch-quantizes multiple float64 vectors with concurrent execution.
// Each vector is converted to float32 before quantization.
func (tq *TurboQuant) QuantizeBatchFloat64(vecs [][]float64) ([]*QuantizedVector, error) {
	f32Vecs := make([][]float32, len(vecs))
	for i, v := range vecs {
		f32Vecs[i] = Float64sToFloat32s(v)
	}
	return tq.QuantizeBatch(f32Vecs)
}

// DequantizeBatchFloat64 batch-dequantizes multiple QuantizedVectors, returning float64 vectors.
// It delegates to DequantizeBatch, then converts each result to float64.
func (tq *TurboQuant) DequantizeBatchFloat64(qvs []*QuantizedVector) ([][]float64, error) {
	f32Results, err := tq.DequantizeBatch(qvs)
	if err != nil {
		return nil, err
	}
	if f32Results == nil {
		return nil, nil
	}
	f64Results := make([][]float64, len(f32Results))
	for i, v := range f32Results {
		f64Results[i] = Float32sToFloat64s(v)
	}
	return f64Results, nil
}

// DequantizeBatch dequantizes multiple QuantizedVectors concurrently using a worker pool.
// Concurrency is controlled by the WithConcurrency option (default: runtime.NumCPU()).
func (tq *TurboQuant) DequantizeBatch(qvs []*QuantizedVector) ([][]float32, error) {
	if len(qvs) == 0 {
		return nil, nil
	}

	results := make([][]float32, len(qvs))
	errs := make([]error, len(qvs))

	var wg sync.WaitGroup
	sem := make(chan struct{}, tq.concurrency)

	for i, qv := range qvs {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, q *QuantizedVector) {
			defer func() { <-sem; wg.Done() }()
			vec, err := dequantizeVector(q, tq.rotation, tq.codebook)
			if err != nil {
				errs[idx] = err
				return
			}
			results[idx] = vec
		}(i, qv)
	}

	wg.Wait()

	// Return the first error encountered.
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}

	return results, nil
}

// SerializeTo writes a QuantizedVector directly to an io.Writer using the compact binary format.
func (tq *TurboQuant) SerializeTo(qv *QuantizedVector, w io.Writer) error {
	return SerializeQuantizedVectorTo(qv, tq.bitWidth, w)
}

// DeserializeFrom reads and deserializes a QuantizedVector from an io.Reader.
func (tq *TurboQuant) DeserializeFrom(r io.Reader) (*QuantizedVector, error) {
	return DeserializeQuantizedVectorFrom(r, tq.bitWidth, tq.dimension)
}

// SerializeBatchTo writes multiple QuantizedVectors sequentially to an io.Writer.
// Format: 4-byte uint32 count (little-endian) followed by count serialized vectors.
func (tq *TurboQuant) SerializeBatchTo(qvs []*QuantizedVector, w io.Writer) error {
	// Write count header.
	var countBuf [4]byte
	binary.LittleEndian.PutUint32(countBuf[:], uint32(len(qvs)))
	if _, err := w.Write(countBuf[:]); err != nil {
		return fmt.Errorf("SerializeBatchTo: failed to write count: %w", err)
	}

	// Write each vector.
	for i, qv := range qvs {
		if err := SerializeQuantizedVectorTo(qv, tq.bitWidth, w); err != nil {
			return fmt.Errorf("SerializeBatchTo: failed to write vector at index %d: %w", i, err)
		}
	}
	return nil
}

// DeserializeBatchFrom reads multiple QuantizedVectors from an io.Reader.
// Expects a 4-byte uint32 count header followed by that many serialized vectors.
func (tq *TurboQuant) DeserializeBatchFrom(r io.Reader) ([]*QuantizedVector, error) {
	// Read count header.
	var countBuf [4]byte
	if _, err := io.ReadFull(r, countBuf[:]); err != nil {
		return nil, fmt.Errorf("DeserializeBatchFrom: failed to read count: %w", err)
	}
	count := binary.LittleEndian.Uint32(countBuf[:])

	// Read each vector.
	qvs := make([]*QuantizedVector, count)
	for i := range count {
		qv, err := DeserializeQuantizedVectorFrom(r, tq.bitWidth, tq.dimension)
		if err != nil {
			return nil, fmt.Errorf("DeserializeBatchFrom: failed to read vector at index %d: %w", i, err)
		}
		qvs[i] = qv
	}
	return qvs, nil
}

// PrepareQuery rotates and normalizes a float32 query vector once for subsequent fast scoring.
func (tq *TurboQuant) PrepareQuery(query []float32) ([]float64, error) {
	dim := len(query)
	if dim != tq.dimension {
		return nil, fmt.Errorf("dimension mismatch: query length %d, expected %d", dim, tq.dimension)
	}

	var sumSq float64
	for _, v := range query {
		f := float64(v)
		sumSq += f * f
	}
	norm := math.Sqrt(sumSq)
	if norm == 0 {
		return make([]float64, dim), nil
	}

	normalized := make([]float64, dim)
	for i, v := range query {
		normalized[i] = float64(v) / norm
	}

	rotated := make([]float64, dim)
	tq.rotation.ApplyInto(normalized, rotated)
	return rotated, nil
}

// ScorePrepared scores a quantized vector against a pre-rotated query vector.
func (tq *TurboQuant) ScorePrepared(preparedQuery []float64, qv *QuantizedVector) float64 {
	if len(qv.Indices) != len(preparedQuery) {
		return 0.0
	}
	var dot float64
	var normSq float64
	centroids := tq.codebook.Centroids
	for i, idx := range qv.Indices {
		val := centroids[idx]
		dot += preparedQuery[i] * val
		normSq += val * val
	}
	if normSq == 0 {
		return 0.0
	}
	return dot / math.Sqrt(normSq)
}
