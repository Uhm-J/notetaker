package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/user/discord-notetaker/internal/audio"
	"github.com/rs/zerolog/log"
)

type FileStore struct {
	baseDir string
}

func NewFileStore(baseDir string) (*FileStore, error) {
	// Create directories if they don't exist
	transcriptDir := filepath.Join(baseDir, "transcripts")
	notesDir := filepath.Join(baseDir, "notes")

	if err := os.MkdirAll(transcriptDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create transcript directory: %w", err)
	}

	if err := os.MkdirAll(notesDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create notes directory: %w", err)
	}

	return &FileStore{
		baseDir: baseDir,
	}, nil
}

func (s *FileStore) SaveTranscript(sessionID string, utterances []audio.Utterance) (string, error) {
	filename := fmt.Sprintf("%s.jsonl", sessionID)
	filepath := filepath.Join(s.baseDir, "transcripts", filename)

	file, err := os.Create(filepath)
	if err != nil {
		return "", fmt.Errorf("failed to create transcript file: %w", err)
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	for _, utterance := range utterances {
		if err := encoder.Encode(utterance); err != nil {
			return "", fmt.Errorf("failed to encode utterance: %w", err)
		}
	}

	log.Info().
		Str("session_id", sessionID).
		Str("file", filepath).
		Int("utterances", len(utterances)).
		Msg("Saved transcript")

	return filepath, nil
}

func (s *FileStore) SaveNotes(sessionID string, notes string) (string, error) {
	filename := fmt.Sprintf("%s.md", sessionID)
	filepath := filepath.Join(s.baseDir, "notes", filename)

	if err := os.WriteFile(filepath, []byte(notes), 0644); err != nil {
		return "", fmt.Errorf("failed to write notes file: %w", err)
	}

	log.Info().
		Str("session_id", sessionID).
		Str("file", filepath).
		Int("size", len(notes)).
		Msg("Saved notes")

	return filepath, nil
}

func (s *FileStore) LoadTranscript(sessionID string) ([]audio.Utterance, error) {
	filename := fmt.Sprintf("%s.jsonl", sessionID)
	filepath := filepath.Join(s.baseDir, "transcripts", filename)

	file, err := os.Open(filepath)
	if err != nil {
		return nil, fmt.Errorf("failed to open transcript file: %w", err)
	}
	defer file.Close()

	var utterances []audio.Utterance
	decoder := json.NewDecoder(file)

	for decoder.More() {
		var utterance audio.Utterance
		if err := decoder.Decode(&utterance); err != nil {
			return nil, fmt.Errorf("failed to decode utterance: %w", err)
		}
		utterances = append(utterances, utterance)
	}

	return utterances, nil
}

func GenerateSessionID() string {
	return fmt.Sprintf("session_%s", time.Now().Format("20060102_150405"))
}