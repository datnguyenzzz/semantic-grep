package splitter

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSplitGoFileWithTypeSpecs(t *testing.T) {
	// Create a temp directory
	tmpDir, err := os.MkdirTemp("", "splitter-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// A highly complex Go file containing various declarations and structures
	goCode := `package main

import "fmt"

const (
	MaxRetries = 3
	TimeoutSec = 10
)

var (
	DefaultCategory = "personal"
	Enabled         = true
)

// User defines a complex user profile
type (
	User struct {
		ID        string
		Name      string
		Metadata  map[string]string
		CreatedBy *User
	}

	// Service defines an interface for managing memories
	Service interface {
		Save(id string, data []byte) error
		Search(query string) ([]string, error)
	}
)

/*
NewUser is a factory function that returns a new User pointer
*/
func NewUser(id, name string) *User {
	return &User{
		ID:   id,
		Name: name,
	}
}

// GetName is a simple value receiver method
func (u User) GetName() string {
	return u.Name
}

// UpdateName is a pointer receiver method that updates user's name
func (u *User) UpdateName(newName string) {
	u.Name = newName
}

func main() {
	user := NewUser("1", "Alice")
	fmt.Printf("Created user: %s\n", user.GetName())
}
`
	filePath := filepath.Join(tmpDir, "main.go")
	err = os.WriteFile(filePath, []byte(goCode), 0644)
	if err != nil {
		t.Fatalf("failed to write main.go: %v", err)
	}

	chunks, err := SplitFile(filePath)
	if err != nil {
		t.Fatalf("failed to split Go file: %v", err)
	}

	// We expect exactly 8 chunks:
	// 0. const block
	// 1. var block
	// 2. User struct (TypeSpec 1)
	// 3. Service interface (TypeSpec 2)
	// 4. NewUser function (FuncDecl 1)
	// 5. GetName method (FuncDecl 2)
	// 6. UpdateName method (FuncDecl 3)
	// 7. main function (FuncDecl 4)
	if len(chunks) != 8 {
		t.Fatalf("expected exactly 8 chunks, got %d", len(chunks))
	}

	expectedRanges := []struct {
		start int
		end   int
	}{
		{5, 8},   // const
		{10, 13}, // var
		{17, 22}, // User struct
		{25, 28}, // Service interface
		{31, 39}, // NewUser function with block comment
		{41, 44}, // GetName method with line comment
		{46, 49}, // UpdateName method with line comment
		{51, 54}, // main function without comment
	}

	for i, expected := range expectedRanges {
		actual := chunks[i]
		if actual.StartLine != expected.start || actual.EndLine != expected.end {
			t.Errorf("Chunk %d line mismatch: expected %d-%d, got %d-%d", i, expected.start, expected.end, actual.StartLine, actual.EndLine)
		}
	}
}

func TestSplitYamlFile(t *testing.T) {
	// Create a temp directory
	tmpDir, err := os.MkdirTemp("", "splitter-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	yamlCode := `apiVersion: v1
kind: Service
metadata:
  name: my-service
spec:
  selector:
    app: nginx
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-deployment
spec:
  replicas: 3
`
	filePath := filepath.Join(tmpDir, "manifest.yaml")
	err = os.WriteFile(filePath, []byte(yamlCode), 0644)
	if err != nil {
		t.Fatalf("failed to write manifest.yaml: %v", err)
	}

	chunks, err := SplitFile(filePath)
	if err != nil {
		t.Fatalf("failed to split YAML file: %v", err)
	}

	// We expect exactly 2 chunks due to the "---" separator
	if len(chunks) != 2 {
		t.Fatalf("expected exactly 2 chunks, got %d", len(chunks))
	}

	if chunks[0].StartLine != 1 || chunks[0].EndLine != 7 {
		t.Errorf("chunk 0 range mismatch: expected 1-7, got %d-%d", chunks[0].StartLine, chunks[0].EndLine)
	}

	if chunks[1].StartLine != 10 || chunks[1].EndLine != 16 {
		t.Errorf("chunk 1 range mismatch: expected 10-16, got %d-%d", chunks[1].StartLine, chunks[1].EndLine)
	}
}

func TestSplitPythonFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "splitter-test-py-*")
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	pyCode := `class Calculator:
    def __init__(self):
        self.value = 0

    def add(self, x):
        self.value += x

def main():
    calc = Calculator()
    calc.add(5)
`
	filePath := filepath.Join(tmpDir, "test.py")
	err = os.WriteFile(filePath, []byte(pyCode), 0644)
	if err != nil {
		t.Fatalf("failed to write test.py: %v", err)
	}

	chunks, err := SplitFile(filePath)
	if err != nil {
		t.Fatalf("failed to split Python file: %v", err)
	}

	// We expect exactly 2 chunks: one for Calculator class, and one for main function!
	if len(chunks) != 2 {
		t.Fatalf("expected exactly 2 chunks, got %d: %v", len(chunks), chunks)
	}

	if chunks[0].StartLine != 1 || chunks[0].EndLine != 6 {
		t.Errorf("expected chunk 0 (class block) to be 1-6, got %d-%d", chunks[0].StartLine, chunks[0].EndLine)
	}

	if chunks[1].StartLine != 8 || chunks[1].EndLine != 10 {
		t.Errorf("expected chunk 1 (function block) to be 8-10, got %d-%d", chunks[1].StartLine, chunks[1].EndLine)
	}
}

func TestSplitPythonFile_WithGlobals(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "splitter-test-py-globals-*")
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	pyCode := `import os
import sys

DEBUG = True

class Logger:
    def log(self, msg):
        if DEBUG:
            print(msg)
`
	filePath := filepath.Join(tmpDir, "logger.py")
	err = os.WriteFile(filePath, []byte(pyCode), 0644)
	if err != nil {
		t.Fatalf("failed to write logger.py: %v", err)
	}

	chunks, err := SplitFile(filePath)
	if err != nil {
		t.Fatalf("failed to split Python file with globals: %v", err)
	}

	// We expect exactly 2 chunks: one for the global header statements, and one for the Logger class!
	if len(chunks) != 2 {
		t.Fatalf("expected exactly 2 chunks, got %d: %v", len(chunks), chunks)
	}

	if chunks[0].StartLine != 1 || chunks[0].EndLine != 5 {
		t.Errorf("expected chunk 0 (globals) to be 1-5, got %d-%d", chunks[0].StartLine, chunks[0].EndLine)
	}

	if chunks[1].StartLine != 6 || chunks[1].EndLine != 9 {
		t.Errorf("expected chunk 1 (class block) to be 6-9, got %d-%d", chunks[1].StartLine, chunks[1].EndLine)
	}
}
