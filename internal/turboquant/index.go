package turboquant

import (
	"sort"
	"sync"
)

type Index struct {
	tq       *TurboQuant
	storage  *Storage
	filePath string
	vectors  map[string]*QuantizedVector
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
	idx.mu.Lock()
	defer idx.mu.Unlock()

	qv, err := idx.tq.Quantize(vec)
	if err != nil {
		return err
	}
	idx.vectors[id] = qv
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

	var results []SearchResult
	for id, qv := range idx.vectors {
		if activeIDs != nil && !activeIDs[id] {
			continue
		}

		sim := idx.tq.ScorePrepared(preparedQuery, qv)
		results = append(results, SearchResult{
			ID:         id,
			Similarity: sim,
		})
	}

	// Sort descending by similarity
	sort.Slice(results, func(i, j int) bool {
		return results[i].Similarity > results[j].Similarity
	})

	if len(results) > limit {
		results = results[:limit]
	}

	return results, nil
}
