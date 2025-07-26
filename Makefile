.PHONY: build run clean test deps

# Build the application
build:
	go build -o bin/discord-notetaker ./cmd/discord-notetaker

# Run the application
run:
	go run ./cmd/discord-notetaker

# Clean build artifacts
clean:
	rm -rf bin/
	rm -rf data/

# Run tests
test:
	go test ./...

# Download dependencies
deps:
	go mod download
	go mod tidy

# Setup project
setup: deps
	mkdir -p data/transcripts data/notes models/vosk assets/sounds
	@echo "Project setup complete!"
	@echo ""
	@echo "Next steps:"
	@echo "1. Copy .env.example to .env and fill in your API keys"
	@echo "2. Download a Vosk model to models/vosk/ (if using Vosk)"
	@echo "3. Run 'make run' to start the bot"

# Development build with race detection
dev:
	go run -race ./cmd/discord-notetaker

# Install for production
install:
	go build -ldflags="-s -w" -o /usr/local/bin/discord-notetaker ./cmd/discord-notetaker