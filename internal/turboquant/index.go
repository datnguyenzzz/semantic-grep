package turboquant

import (
	"math"
	"sync"
)

type Index struct {
	tq       *TurboQuant
	storage  *Storage
	filePath string
	vectors  map[string][]byte
	mu       sync.RWMutex
}

func NewIndex(filePath string, tq *TurboQuant) (*Index, error) {
	s := NewStorage(tq.dimension, tq.bitWidth)
	vectors, err := s.Load(filePath, tq)
	if err != nil {
		return nil, err
	}

	return &Index{
		tq:       tq,
		storage:  s,
		filePath: filePath,
		vectors:  vectors,
	}, nil
}

func (idx *Index) Add(id string, vec []float32) error {
	// ponytail: perform CPU-heavy rotation and quantization outside the lock to enable multi-core parallelism
	qv, err := idx.tq.Quantize(vec)
	if err != nil {
		return err
	}
	serialized, err := idx.tq.Serialize(qv)
	if err != nil {
		return err
	}

	idx.mu.Lock()
	idx.vectors[id] = serialized
	idx.mu.Unlock()
	return nil
}

func (idx *Index) Delete(id string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	delete(idx.vectors, id)
}

func (idx *Index) Save() error {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.storage.Save(idx.filePath, idx.tq, idx.vectors)
}

func (idx *Index) Compact(activeIDs map[string]bool) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	// Filter in-memory map to only contain active IDs
	for id := range idx.vectors {
		if !activeIDs[id] {
			delete(idx.vectors, id)
		}
	}

	return idx.storage.Save(idx.filePath, idx.tq, idx.vectors)
}

type SearchResult struct {
	ID         string
	Similarity float64
}

func (idx *Index) Search(query []float32, activeIDs map[string]bool, limit int) ([]SearchResult, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if len(idx.vectors) == 0 {
		return nil, nil
	}

	// Prepare/rotate the query embedding exactly once to bypass expensive per-vector dequantization
	preparedQuery, err := idx.tq.PrepareQuery(query)
	if err != nil {
		return nil, err
	}

	// Compute query suffix energy array for fast early pruning checks
	querySuffixEnergy := make([]float64, len(preparedQuery))
	var sum float64
	for i := len(preparedQuery) - 1; i >= 0; i-- {
		sum += preparedQuery[i] * preparedQuery[i]
		querySuffixEnergy[i] = math.Sqrt(sum)
	}

	maxCentroid, minCentroidSq := idx.tq.GetCentroidBounds()

	// Keep a dynamic list of results to track the running threshold
	var results []SearchResult
	for id, serialized := range idx.vectors {
		if activeIDs != nil && !activeIDs[id] {
			continue
		}

		qv, err := idx.tq.Deserialize(serialized)
		if err != nil {
			continue
		}

		// Calculate threshold: if we already have 'limit' results, the threshold is the worst similarity among them
		threshold := -1.0
		if len(results) >= limit {
			threshold = results[len(results)-1].Similarity
		}

		sim, pruned := idx.tq.ScorePreparedWithPruning(preparedQuery, querySuffixEnergy, maxCentroid, minCentroidSq, threshold, qv)
		if pruned {
			continue // Candidate pruned!
		}

		// Insert candidate at its sorted position
		res := SearchResult{ID: id, Similarity: sim}
		inserted := false
		for i, existing := range results {
			if sim > existing.Similarity {
				results = append(results[:i], append([]SearchResult{res}, results[i:]...)...)
				inserted = true
				break
			}
		}
		if !inserted {
			results = append(results, res)
		}

		// Keep size capped at limit
		if len(results) > limit {
			results = results[:limit]
		}
	}

	return results, nil
}

// VectorsCount returns the number of vectors currently loaded in memory.
func (idx *Index) VectorsCount() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.vectors)
}

// MemorySizeBytes calculates the exact memory footprint of all serialized vectors stored in RAM.
func (idx *Index) MemorySizeBytes() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	total := 0
	for id, serialized := range idx.vectors {
		total += len(id) + len(serialized)
	}
	return total
}
