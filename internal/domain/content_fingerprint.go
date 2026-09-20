package domain

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash/fnv"
	"math/bits"
	"strings"
	"time"
)

// DocumentFingerprint is the narrow shape application.RunContentDedupJob
// needs per document -- just enough to group duplicates and report a
// human-readable merge result, not the full Document (text/embeddings
// would be wasted memory across an entire corpus scan).
type DocumentFingerprint struct {
	ID          string
	URL         string
	Host        string
	ContentHash string
	SimHash     string
	CrawledAt   time.Time
}

// ContentHash returns a stable fingerprint of text's content, case-folded
// (htmlparser.Parse already collapses whitespace). Two documents with an
// identical ContentHash are the exact-match dedup case.
func ContentHash(text string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(text)))
	return hex.EncodeToString(sum[:])
}

// simHashShingleSize is the word-shingle width SimHash64 hashes over -- 3
// words is the standard, well-tested choice for near-duplicate web page
// detection (long enough that common short phrases don't collide
// constantly, short enough that a handful of edited words don't change
// most shingles).
const simHashShingleSize = 3

// SimHash64 returns a 64-bit locality-sensitive fingerprint of text
// (16 hex chars -- dialect-portable, unlike a signed BIGINT). Fingerprints
// differing in only a few bits (HammingDistance64) mean near-duplicate
// text: each fnv-1a-hashed word shingle casts a per-bit majority vote,
// so similar texts share most shingles and stay close in Hamming distance.
func SimHash64(text string) uint64 {
	words := strings.Fields(strings.ToLower(text))
	var votes [64]int
	shingleCount := 0
	shingle := func(ws []string) uint64 {
		h := fnv.New64a()
		_, _ = h.Write([]byte(strings.Join(ws, " ")))
		return h.Sum64()
	}
	addVotes := func(h uint64) {
		shingleCount++
		for bit := 0; bit < 64; bit++ {
			if h&(1<<uint(bit)) != 0 {
				votes[bit]++
			} else {
				votes[bit]--
			}
		}
	}
	if len(words) < simHashShingleSize {
		if len(words) > 0 {
			addVotes(shingle(words))
		}
	} else {
		for i := 0; i+simHashShingleSize <= len(words); i++ {
			addVotes(shingle(words[i : i+simHashShingleSize]))
		}
	}
	if shingleCount == 0 {
		return 0
	}
	var out uint64
	for bit := 0; bit < 64; bit++ {
		if votes[bit] > 0 {
			out |= 1 << uint(bit)
		}
	}
	return out
}

// EncodeSimHash64/DecodeSimHash64 convert between SimHash64's uint64 value
// and the 16-hex-character string documents.simhash actually stores.
func EncodeSimHash64(h uint64) string {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, h)
	return hex.EncodeToString(b)
}

func DecodeSimHash64(s string) uint64 {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(b)
}

// HammingDistance64 counts the differing bits between two SimHash64
// fingerprints -- the near-duplicate distance metric application.
// RunContentDedupJob compares against OperationalSettingsValues.
// ContentDedupSimHashMaxDistance.
func HammingDistance64(a, b uint64) int {
	return bits.OnesCount64(a ^ b)
}
