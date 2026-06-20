package turboquant

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

// SerializeQuantizedVector serializes a QuantizedVector into a compact binary format.
// Format: [4 bytes float32 norm (little-endian)][bit-packed indices]
// Packing rules:
//   - 2-bit: 4 indices per byte, low bits first
//   - 3-bit: bitstream, indices packed continuously across byte boundaries
//   - 4-bit: 2 indices per byte, low nibble first
func SerializeQuantizedVector(qv *QuantizedVector, bitWidth int) ([]byte, error) {
	if err := ValidateBitWidth(bitWidth); err != nil {
		return nil, err
	}
	if qv == nil {
		return nil, fmt.Errorf("quantized vector is nil")
	}

	dimension := len(qv.Indices)
	indexByteCount := indexBytesNeeded(dimension, bitWidth)
	totalSize := 4 + indexByteCount
	buf := make([]byte, totalSize)

	// Write float32 norm in little-endian.
	binary.LittleEndian.PutUint32(buf[0:4], math.Float32bits(qv.Norm))

	// Pack indices into the remaining bytes.
	indexBuf := buf[4:]
	switch bitWidth {
	case Bit2:
		packIndices2Bit(indexBuf, qv.Indices)
	case Bit3:
		packIndices3Bit(indexBuf, qv.Indices)
	case Bit4:
		packIndices4Bit(indexBuf, qv.Indices)
	}

	return buf, nil
}

// indexBytesNeeded returns the number of bytes needed to store dimension indices at the given bitWidth.
func indexBytesNeeded(dimension, bitWidth int) int {
	switch bitWidth {
	case Bit2:
		return int(math.Ceil(float64(dimension) / 4.0))
	case Bit3:
		return int(math.Ceil(float64(dimension) * 3.0 / 8.0))
	case Bit4:
		return int(math.Ceil(float64(dimension) / 2.0))
	default:
		return 0
	}
}

// packIndices2Bit packs indices into bytes, 4 indices per byte, low bits first.
// Byte = idx[0] | (idx[1]<<2) | (idx[2]<<4) | (idx[3]<<6)
func packIndices2Bit(dst []byte, indices []uint8) {
	n := len(indices)
	for i := 0; i < n; i += 4 {
		var b byte
		b = indices[i] & 0x03
		if i+1 < n {
			b |= (indices[i+1] & 0x03) << 2
		}
		if i+2 < n {
			b |= (indices[i+2] & 0x03) << 4
		}
		if i+3 < n {
			b |= (indices[i+3] & 0x03) << 6
		}
		dst[i/4] = b
	}
}

// packIndices3Bit packs indices as a continuous bitstream across byte boundaries.
func packIndices3Bit(dst []byte, indices []uint8) {
	bitPos := 0
	for _, idx := range indices {
		val := idx & 0x07 // mask to 3 bits
		byteIdx := bitPos / 8
		bitOffset := bitPos % 8

		// Place the 3 bits starting at bitOffset within the current byte.
		// May span into the next byte.
		dst[byteIdx] |= val << uint(bitOffset)
		if bitOffset+3 > 8 {
			dst[byteIdx+1] |= val >> uint(8-bitOffset)
		}
		bitPos += 3
	}
}

// packIndices4Bit packs indices into bytes, 2 indices per byte, low nibble first.
// Byte = idx[0] | (idx[1]<<4)
func packIndices4Bit(dst []byte, indices []uint8) {
	n := len(indices)
	for i := 0; i < n; i += 2 {
		b := indices[i] & 0x0F
		if i+1 < n {
			b |= (indices[i+1] & 0x0F) << 4
		}
		dst[i/2] = b
	}
}

// DeserializeQuantizedVector deserializes a compact binary byte slice back into a QuantizedVector.
// Format: [4 bytes float32 norm (little-endian)][bit-packed indices]
// Returns a format error if the byte slice length does not match the expected size.
func DeserializeQuantizedVector(data []byte, bitWidth, dimension int) (*QuantizedVector, error) {
	if err := ValidateBitWidth(bitWidth); err != nil {
		return nil, err
	}
	if dimension < 0 {
		return nil, fmt.Errorf("invalid dimension %d: must be non-negative", dimension)
	}
	if dimension == 0 {
		if len(data) != 4 {
			return nil, fmt.Errorf("invalid data length: got %d bytes, expected 4 bytes for dimension=0", len(data))
		}
		norm := math.Float32frombits(binary.LittleEndian.Uint32(data[0:4]))
		return &QuantizedVector{Norm: norm, Indices: []uint8{}}, nil
	}

	expectedSize := 4 + indexBytesNeeded(dimension, bitWidth)
	if len(data) != expectedSize {
		return nil, fmt.Errorf("invalid data length: got %d bytes, expected %d bytes for dimension=%d bitWidth=%d", len(data), expectedSize, dimension, bitWidth)
	}

	// Read float32 norm from first 4 bytes (little-endian).
	norm := math.Float32frombits(binary.LittleEndian.Uint32(data[0:4]))

	// Unpack indices from remaining bytes.
	indexBuf := data[4:]
	var indices []uint8
	switch bitWidth {
	case Bit2:
		indices = unpackIndices2Bit(indexBuf, dimension)
	case Bit3:
		indices = unpackIndices3Bit(indexBuf, dimension)
	case Bit4:
		indices = unpackIndices4Bit(indexBuf, dimension)
	}

	return &QuantizedVector{
		Norm:    norm,
		Indices: indices,
	}, nil
}

func DeserializeQuantizedVectorBuf(data []byte, bitWidth, dimension int, dst []uint8) (float32, error) {
	if len(data) < 4 {
		return 0, fmt.Errorf("data too short")
	}
	norm := math.Float32frombits(binary.LittleEndian.Uint32(data[0:4]))
	indexBuf := data[4:]

	switch bitWidth {
	case Bit2:
		for i := range dimension {
			byteIdx := i / 4
			shift := uint(i%4) * 2
			dst[i] = (indexBuf[byteIdx] >> shift) & 0x03
		}
	case Bit3:
		bitPos := 0
		for i := range dimension {
			byteIdx := bitPos / 8
			bitOffset := uint(bitPos % 8)
			val := indexBuf[byteIdx] >> bitOffset
			if bitOffset+3 > 8 && byteIdx+1 < len(indexBuf) {
				val |= indexBuf[byteIdx+1] << (8 - bitOffset)
			}
			dst[i] = val & 0x07
			bitPos += 3
		}
	case Bit4:
		for i := 0; i < dimension; i += 2 {
			b := indexBuf[i/2]
			dst[i] = b & 0x0F
			if i+1 < dimension {
				dst[i+1] = b >> 4
			}
		}
	}
	return norm, nil
}

// unpackIndices2Bit extracts dimension indices from bytes, 4 indices per byte, low bits first.
func unpackIndices2Bit(src []byte, dimension int) []uint8 {
	indices := make([]uint8, dimension)
	for i := 0; i < dimension; i++ {
		byteIdx := i / 4
		shift := uint(i%4) * 2
		indices[i] = (src[byteIdx] >> shift) & 0x03
	}
	return indices
}

// unpackIndices3Bit extracts dimension indices from a continuous bitstream, 3 bits per index.
func unpackIndices3Bit(src []byte, dimension int) []uint8 {
	indices := make([]uint8, dimension)
	bitPos := 0
	for i := 0; i < dimension; i++ {
		byteIdx := bitPos / 8
		bitOffset := uint(bitPos % 8)

		// Extract 3 bits, potentially spanning two bytes.
		val := src[byteIdx] >> bitOffset
		if bitOffset+3 > 8 && byteIdx+1 < len(src) {
			val |= src[byteIdx+1] << (8 - bitOffset)
		}
		indices[i] = val & 0x07
		bitPos += 3
	}
	return indices
}

// unpackIndices4Bit extracts dimension indices from bytes, 2 indices per byte, low nibble first.
func unpackIndices4Bit(src []byte, dimension int) []uint8 {
	indices := make([]uint8, dimension)
	for i := 0; i < dimension; i++ {
		byteIdx := i / 2
		if i%2 == 0 {
			indices[i] = src[byteIdx] & 0x0F
		} else {
			indices[i] = (src[byteIdx] >> 4) & 0x0F
		}
	}
	return indices
}

// SerializeQuantizedVectorTo writes a QuantizedVector directly to an io.Writer
// using the same binary format as SerializeQuantizedVector.
func SerializeQuantizedVectorTo(qv *QuantizedVector, bitWidth int, w io.Writer) error {
	if err := ValidateBitWidth(bitWidth); err != nil {
		return err
	}
	if qv == nil {
		return fmt.Errorf("quantized vector is nil")
	}

	dimension := len(qv.Indices)
	indexByteCount := indexBytesNeeded(dimension, bitWidth)
	totalSize := 4 + indexByteCount
	buf := make([]byte, totalSize)

	// Write float32 norm in little-endian.
	binary.LittleEndian.PutUint32(buf[0:4], math.Float32bits(qv.Norm))

	// Pack indices into the remaining bytes.
	indexBuf := buf[4:]
	switch bitWidth {
	case Bit2:
		packIndices2Bit(indexBuf, qv.Indices)
	case Bit3:
		packIndices3Bit(indexBuf, qv.Indices)
	case Bit4:
		packIndices4Bit(indexBuf, qv.Indices)
	}

	_, err := w.Write(buf)
	return err
}

// DeserializeQuantizedVectorFrom reads and deserializes a QuantizedVector from an io.Reader.
// It uses the same binary format as DeserializeQuantizedVector.
func DeserializeQuantizedVectorFrom(r io.Reader, bitWidth, dimension int) (*QuantizedVector, error) {
	if err := ValidateBitWidth(bitWidth); err != nil {
		return nil, err
	}
	if dimension < 0 {
		return nil, fmt.Errorf("invalid dimension %d: must be non-negative", dimension)
	}

	expectedSize := 4 + indexBytesNeeded(dimension, bitWidth)
	buf := make([]byte, expectedSize)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, fmt.Errorf("failed to read quantized vector: %w", err)
	}

	// Read float32 norm from first 4 bytes (little-endian).
	norm := math.Float32frombits(binary.LittleEndian.Uint32(buf[0:4]))

	if dimension == 0 {
		return &QuantizedVector{Norm: norm, Indices: []uint8{}}, nil
	}

	// Unpack indices from remaining bytes.
	indexBuf := buf[4:]
	var indices []uint8
	switch bitWidth {
	case Bit2:
		indices = unpackIndices2Bit(indexBuf, dimension)
	case Bit3:
		indices = unpackIndices3Bit(indexBuf, dimension)
	case Bit4:
		indices = unpackIndices4Bit(indexBuf, dimension)
	}

	return &QuantizedVector{
		Norm:    norm,
		Indices: indices,
	}, nil
}
