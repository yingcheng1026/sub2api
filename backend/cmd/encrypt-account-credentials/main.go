package main

import (
	"context"
	"flag"
	"log"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
)

func main() {
	dryRun := flag.Bool("dry-run", true, "scan only; set --dry-run=false to rewrite plaintext account credentials")
	timeout := flag.Duration("timeout", 10*time.Minute, "maximum runtime for the encryption pass")
	flag.Parse()

	cfg, err := config.ProvideConfig()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	client, err := repository.ProvideEnt(cfg)
	if err != nil {
		log.Fatalf("connect database: %v", err)
	}
	defer func() { _ = client.Close() }()

	encryptor, err := repository.NewAESEncryptor(cfg)
	if err != nil {
		log.Fatalf("create encryptor: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	result, err := repository.EncryptPlaintextAccountCredentials(ctx, client, encryptor, *dryRun)
	if err != nil {
		log.Fatalf("encrypt account credentials: %v", err)
	}

	log.Printf("account credentials encryption complete: dry_run=%t scanned=%d needs_encryption=%d updated=%d",
		*dryRun,
		result.Scanned,
		result.NeedsEncryption,
		result.Updated,
	)
}
