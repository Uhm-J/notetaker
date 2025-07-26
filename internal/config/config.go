package config

import (
	"fmt"
	"os"
	"strconv"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
)

type Config struct {
	// Discord
	DiscordToken string

	// STT Backend
	STTBackend string // "vosk" or "deepgram"

	// Vosk settings
	VoskModelPath string

	// Deepgram settings
	DeepgramAPIKey    string
	DeepgramTier      string
	DeepgramDiarize   bool
	DeepgramPunctuate bool
	DeepgramUtterances bool

	// Gemini settings
	GenAIAPIKey string
	GenAIBackend string // "gemini" or "vertex"
	GenAIModel  string

	// Chunking settings
	ChunkSeconds    int
	ChunkOverlapMS  int
	MaxParallelSTT  int

	// Logging
	LogLevel string
}

func Load() (*Config, error) {
	// Load .env file if it exists
	if err := godotenv.Load(); err != nil {
		log.Warn().Err(err).Msg("No .env file found, using environment variables only")
	}

	cfg := &Config{
		// Discord
		DiscordToken: os.Getenv("DISCORD_TOKEN"),

		// STT Backend
		STTBackend: getEnvOrDefault("STT_BACKEND", "vosk"),

		// Vosk
		VoskModelPath: getEnvOrDefault("VOSK_MODEL_PATH", "./models/vosk/en"),

		// Deepgram
		DeepgramAPIKey:     os.Getenv("DEEPGRAM_API_KEY"),
		DeepgramTier:       getEnvOrDefault("DEEPGRAM_TIER", "nova-2"),
		DeepgramDiarize:    getBoolEnvOrDefault("DEEPGRAM_DIARIZE", true),
		DeepgramPunctuate:  getBoolEnvOrDefault("DEEPGRAM_PUNCTUATE", true),
		DeepgramUtterances: getBoolEnvOrDefault("DEEPGRAM_UTTERANCES", true),

		// Gemini
		GenAIAPIKey:  os.Getenv("GENAI_API_KEY"),
		GenAIBackend: getEnvOrDefault("GENAI_BACKEND", "gemini"),
		GenAIModel:   getEnvOrDefault("GENAI_MODEL", "gemini-2.5-flash"),

		// Chunking
		ChunkSeconds:   getIntEnvOrDefault("CHUNK_SECONDS", 5),
		ChunkOverlapMS: getIntEnvOrDefault("CHUNK_OVERLAP_MS", 300),
		MaxParallelSTT: getIntEnvOrDefault("MAX_PARALLEL_STT", 4),

		// Logging
		LogLevel: getEnvOrDefault("LOG_LEVEL", "info"),
	}

	return cfg, cfg.validate()
}

func (c *Config) validate() error {
	if c.DiscordToken == "" {
		return fmt.Errorf("DISCORD_TOKEN is required")
	}

	if c.STTBackend != "vosk" && c.STTBackend != "deepgram" {
		return fmt.Errorf("STT_BACKEND must be 'vosk' or 'deepgram'")
	}

	if c.STTBackend == "deepgram" && c.DeepgramAPIKey == "" {
		return fmt.Errorf("DEEPGRAM_API_KEY is required when using deepgram backend")
	}

	if c.GenAIAPIKey == "" {
		return fmt.Errorf("GENAI_API_KEY is required")
	}

	return nil
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getIntEnvOrDefault(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intVal, err := strconv.Atoi(value); err == nil {
			return intVal
		}
	}
	return defaultValue
}

func getBoolEnvOrDefault(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		if boolVal, err := strconv.ParseBool(value); err == nil {
			return boolVal
		}
	}
	return defaultValue
}