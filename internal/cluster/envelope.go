// Package cluster embeds the ToyRaft consensus library to give toykv a
// replicated command path (ROADMAP §M18). A mutating command is encoded into a
// deterministic envelope, proposed through Raft, and re-executed on every node
// via the server's dispatch chokepoint when the entry is applied.
//
// M18 runs a single-node cluster: the local node is trivially the leader, the
// Raft log lives in memory, and the AOF remains the durability source (state
// re-derives from the AOF on restart). Multi-node transport, file-backed Raft
// storage, and election arrive in M19.
package cluster

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// envelopeVersion is the leading byte of every encoded command envelope. It
// lets the wire format evolve (e.g. a future compression or checksum framing)
// while old-format entries in a replayed log stay decodable — the same
// version-byte discipline the AOF format uses.
const envelopeVersion byte = 0x01

// Encode serialises argv into a deterministic byte slice suitable for a Raft
// log entry: a version byte followed by the RESP array-of-bulk-strings encoding
// of the command, mirroring the AOF record format (aof.Writer.Append) so both
// durability surfaces speak the same bytes. The encoding is a pure function of
// argv — identical argv always produces identical bytes — which is what makes
// StateMachine.Apply deterministic across replicas.
func Encode(argv [][]byte) []byte {
	elems := make([]resp.Value, len(argv))
	for i, a := range argv {
		elems[i] = resp.Bulk(a)
	}
	var buf bytes.Buffer
	buf.WriteByte(envelopeVersion)
	w := resp.NewWriter(&buf)
	// resp.Writer only returns an error from the underlying io.Writer; a
	// bytes.Buffer never fails, so these cannot error in practice.
	_ = w.WriteFrame(resp.Array(elems...))
	_ = w.Flush()
	return buf.Bytes()
}

// Decode reverses Encode, returning the command argv. It rejects an empty
// buffer, an unknown version byte, and any RESP framing error — a malformed
// entry must surface, never be silently dropped.
func Decode(data []byte) ([][]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("cluster: empty envelope")
	}
	if data[0] != envelopeVersion {
		return nil, fmt.Errorf("cluster: unknown envelope version 0x%02x", data[0])
	}
	argv, err := resp.NewReader(bytes.NewReader(data[1:])).ReadCommand()
	if err != nil {
		return nil, fmt.Errorf("cluster: decode envelope: %w", err)
	}
	return argv, nil
}
