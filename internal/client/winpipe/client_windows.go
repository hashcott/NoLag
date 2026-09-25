//go:build windows

package winpipe

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Microsoft/go-winio"

	"gamenolag/internal/client/ipc"
)

// ErrNoService means the pipe is not there: the service is not running, or this
// user is not admitted by its access control.
var ErrNoService = fmt.Errorf("winpipe: the GameNoLag service is not reachable")

// Ask sends one verb and reads the answer.
//
// One request per connection, which is what the server expects: the interface
// opens the pipe, asks, reads and closes. Holding it open to send a stream would
// let one caller occupy the privilege boundary indefinitely.
func Ask(ctx context.Context, v ipc.Verb) (ipc.Response, error) {
	conn, err := winio.DialPipeContext(ctx, PipeName)
	if err != nil {
		return ipc.Response{}, fmt.Errorf("%w: %v", ErrNoService, err)
	}
	defer conn.Close()

	// A deadline on both halves. The service holds a lock across a connect, which
	// can take as long as the measurement window, so this is generous — but it is
	// finite, because an interface frozen on a read is worse than one that says
	// the service is not answering.
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(90 * time.Second))
	}

	body, err := ipc.Encode(ipc.Request{Verb: v})
	if err != nil {
		return ipc.Response{}, err
	}
	if _, err := conn.Write(body); err != nil {
		return ipc.Response{}, fmt.Errorf("winpipe: sending %s: %w", v, err)
	}

	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 1024), ipc.MaxLineBytes)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return ipc.Response{}, fmt.Errorf("winpipe: reading the answer to %s: %w", v, err)
		}
		return ipc.Response{}, fmt.Errorf("winpipe: the service closed the pipe without answering %s", v)
	}
	var resp ipc.Response
	if err := json.Unmarshal(sc.Bytes(), &resp); err != nil {
		return ipc.Response{}, fmt.Errorf("winpipe: the answer to %s was not the expected JSON: %w", v, err)
	}
	return resp, nil
}
