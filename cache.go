package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/mark3labs/mcp-go/mcp"
)

const toolCacheVersion = 1

type cachedTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
}

type cachedServer struct {
	Fingerprint string       `json:"fingerprint"`
	Tools       []cachedTool `json:"tools"`
}

type toolCache struct {
	Version int                     `json:"version"`
	Servers map[string]cachedServer `json:"servers"`
}

func toolCachePath() string {
	return filepath.Join(zotHome(), "mcp-tools-cache.json")
}

func serverFingerprint(cfg ServerConfig) string {
	data, _ := json.Marshal(cfg)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func readToolCache(path string) (toolCache, error) {
	cache := toolCache{Version: toolCacheVersion, Servers: map[string]cachedServer{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cache, nil
		}
		return cache, err
	}
	if err := json.Unmarshal(data, &cache); err != nil {
		return cache, fmt.Errorf("parse %s: %w", path, err)
	}
	if cache.Version != toolCacheVersion || cache.Servers == nil {
		return toolCache{Version: toolCacheVersion, Servers: map[string]cachedServer{}}, nil
	}
	return cache, nil
}

func toolCachesEqual(a, b toolCache) bool {
	aj, err := json.Marshal(a)
	if err != nil {
		return false
	}
	bj, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return string(aj) == string(bj)
}

func writeToolCache(path string, cache toolCache) error {
	if cache.Servers == nil {
		cache.Servers = map[string]cachedServer{}
	}
	cache.Version = toolCacheVersion
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeFileAtomic(path, data, 0o600)
}

func cachedToolFromMCP(serverName string, tool mcp.Tool) (cachedTool, error) {
	schema := mcpToolSchema(tool)
	schemaJSON, err := json.Marshal(schema)
	if err != nil {
		return cachedTool{}, err
	}
	return cachedTool{
		Name:        tool.Name,
		Description: mcpToolDescription(serverName, tool),
		Schema:      json.RawMessage(schemaJSON),
	}, nil
}
