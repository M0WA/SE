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
	if len(words) < simHashShingleSize {
		if len(words) > 0 {
			addSimHashVotes(&votes, simHashOfShingle(words))
			shingleCount++
		}
	} else {
		for i := 0; i+simHashShingleSize <= len(words); i++ {
			addSimHashVotes(&votes, simHashOfShingle(words[i:i+simHashShingleSize]))
			shingleCount++
		}
	}
	if shingleCount == 0 {
		return 0
	}
	return simHashFromVotes(votes)
}

// simHashOfShingle fnv-1a-hashes one word shingle -- shared by both of
// SimHash64's branches (a too-short text hashed whole, or each sliding
// window of a longer one).
func simHashOfShingle(ws []string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(strings.Join(ws, " ")))
	return h.Sum64()
}

// addSimHashVotes casts one shingle hash's per-bit majority vote into
// votes: +1 for each set bit, -1 for each clear one.
func addSimHashVotes(votes *[64]int, h uint64) {
	for bit := 0; bit < 64; bit++ {
		if h&(1<<uint(bit)) != 0 {
			votes[bit]++
		} else {
			votes[bit]--
		}
	}
}

// simHashFromVotes collapses the accumulated per-bit votes into the final
// fingerprint: a bit is set iff its vote total is positive.
func simHashFromVotes(votes [64]int) uint64 {
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
