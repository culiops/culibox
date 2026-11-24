// Package boximagecheck tells an instance which box image it runs and whether the
// image CI built from this binary's Dockerfile is a different one. It describes;
// it never pulls, tags or edits anything.
package boximagecheck

import (
	"time"

	"github.com/culiops/culibox/internal/boxregistry"
)

// Scan is the weekly vulnerability scan's open issue: its number and the count of
// fixable HIGH or CRITICAL findings its title states. Nothing else of the issue is kept.
type Scan struct {
	Number int `json:"number"`
	Count  int `json:"count"`
}

// Cache is what the daily check learned, shared by the server and doctor
// (<dataDir>/image-check.json). Hash is the Dockerfile hash of the binary that
// asked: an answer for another Dockerfile is not this binary's answer.
type Cache struct {
	Version     int                `json:"version"`
	CheckedAt   time.Time          `json:"checked_at"`
	Hash        string             `json:"hash"`
	Registry    *boxregistry.Index `json:"registry,omitempty"`
	RegistryErr string             `json:"registry_err,omitempty"` // a short class, never a raw body
	Scan        *Scan              `json:"scan,omitempty"`
	ScanErr     string             `json:"scan_err,omitempty"`
}
