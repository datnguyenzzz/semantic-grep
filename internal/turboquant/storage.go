package turboquant

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// Header size is 16 bytes: magic [4]byte ("TQLM") + version uint32 + num_vectors uint32 + bytes_per_vector uint32
const HeaderSize = 16

// Metadata size is 5 bytes: dimension uint32 + bit_width uint8
const MetadataSize = 5

type Storage struct {
	dimension      int
	bitWidth       int
	bytesPerVector int
}

func NewStorage(dimension, bitWidth int) *Storage {
	bytesPerVector := 36 + 4 + indexBytesNeeded(dimension, bitWidth)
	return &Storage{
		dimension:      dimension,
		bitWidth:       bitWidth,
		bytesPerVector: bytesPerVector,
	}
}

func (s *Storage) Load(filePath string, tq *TurboQuant) (map[string]*QuantizedVector, error) {
	f, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]*QuantizedVector), nil
		}
		return nil, err
	}
	defer f.Close()

	header := make([]byte, HeaderSize)
	if _, err := io.ReadFull(f, header); err != nil {
		return nil, fmt.Errorf("failed to read storage header: %w", err)
	}

	if string(header[0:4]) != "TQLM" {
		return nil, fmt.Errorf("invalid storage magic header")
	}

	numVectors := int(binary.LittleEndian.Uint32(header[8:12]))
	bytesPerVector := int(binary.LittleEndian.Uint32(header[12:16]))

	meta := make([]byte, MetadataSize)
	if _, err := io.ReadFull(f, meta); err != nil {
		return nil, fmt.Errorf("failed to read storage metadata: %w", err)
	}

	// Rebuild vectors map
	vectors := make(map[string]*QuantizedVector, numVectors)
	buf := make([]byte, bytesPerVector)

	for i := 0; i < numVectors; i++ {
		_, err := io.ReadFull(f, buf)
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("failed to read vector record %d: %w", i, err)
		}

		// Extract ID and trim spaces/padding
		idEnd := 36
		for idEnd > 0 && buf[idEnd-1] == 0 {
			idEnd--
		}
		id := string(buf[0:idEnd])

		serialized := make([]byte, bytesPerVector-36)
		copy(serialized, buf[36:])

		qv, err := tq.Deserialize(serialized)
		if err != nil {
			continue
		}

		vectors[id] = qv
	}

	return vectors, nil
}

func (s *Storage) Save(filePath string, tq *TurboQuant, vectors map[string]*QuantizedVector) error {
	// Re-write the file fresh to prevent accumulation of deleted items
	tmpFile := filePath + ".tmp"
	_ = os.Remove(tmpFile)

	f, err := os.OpenFile(tmpFile, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	// Write header
	header := make([]byte, HeaderSize)
	copy(header[0:4], []byte("TQLM"))
	binary.LittleEndian.PutUint32(header[4:8], 1) // version
	binary.LittleEndian.PutUint32(header[8:12], uint32(len(vectors)))
	binary.LittleEndian.PutUint32(header[12:16], uint32(s.bytesPerVector))

	if _, err := f.Write(header); err != nil {
		return err
	}

	// Write metadata
	meta := make([]byte, MetadataSize)
	binary.LittleEndian.PutUint32(meta[0:4], uint32(s.dimension))
	meta[4] = uint8(s.bitWidth)

	if _, err := f.Write(meta); err != nil {
		return err
	}

	// Write all records
	recordBuf := make([]byte, s.bytesPerVector)
	for id, qv := range vectors {
		serialized, err := tq.Serialize(qv)
		if err != nil {
			return err
		}

		idBytes := make([]byte, 36)
		copy(idBytes, []byte(id))
		copy(recordBuf[0:36], idBytes)
		copy(recordBuf[36:], serialized)

		if _, err := f.Write(recordBuf); err != nil {
			return err
		}
	}

	f.Close()
	return os.Rename(tmpFile, filePath)
}
