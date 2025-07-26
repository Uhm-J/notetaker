package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/user/discord-notetaker/internal/bot"
	"github.com/user/discord-notetaker/internal/config"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func main() {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to load configuration")
	}

	// Setup logging
	setupLogging(cfg.LogLevel)

	log.Info().Msg("Starting Discord Notetaker Bot")

	// Create bot
	discordBot, err := bot.NewBot(cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create bot")
	}

	// Start bot
	if err := discordBot.Start(); err != nil {
		log.Fatal().Err(err).Msg("Failed to start bot")
	}

	// Wait for shutdown signal
	log.Info().Msg("Bot is running. Press Ctrl+C to exit.")
	
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c

	log.Info().Msg("Shutting down bot...")

	// Graceful shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- discordBot.Stop()
	}()

	select {
	case err := <-done:
		if err != nil {
			log.Error().Err(err).Msg("Error during shutdown")
		} else {
			log.Info().Msg("Bot stopped gracefully")
		}
	case <-ctx.Done():
		log.Warn().Msg("Shutdown timeout exceeded, forcing exit")
	}
}

func setupLogging(level string) {
	// Setup zerolog
	zerolog.TimeFieldFormat = time.RFC3339
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"})

	// Set log level
	switch level {
	case "debug":
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	case "info":
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	case "warn":
		zerolog.SetGlobalLevel(zerolog.WarnLevel)
	case "error":
		zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	default:
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}

	log.Info().Str("level", level).Msg("Logging configured")
}