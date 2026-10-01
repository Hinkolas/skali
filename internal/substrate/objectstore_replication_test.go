package substrate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
	"github.com/Hinkolas/skali/internal/testdb"
)

// fakeVolume is one volume as the master lists it: where it sits and
// which replication code its placement carries.
type fakeVolume struct {
	ID   int64
	Node string
	Code string
}

// replicationDoer is the seaweed transport for the replication tests: it
// keeps filer.conf in memory (absent until the first PUT) and renders the
// master's /vol/status from the volumes, and its exec channel records the
// shell scripts, fails a configurable number of times, and otherwise moves
// every volume not listed as stuck to the code the script asks for, the
// way volume.configure.replication does on a healthy volume server.
type replicationDoer struct {
	conf     []byte
	confPuts int
	volumes  []fakeVolume
	scripts  []string
	failures int
	stuck    map[int64]bool
	output   string
}

var replicationArg = regexp.MustCompile(`-replication=(\d{3})`)

func (d *replicationDoer) ServiceProxyDo(_ context.Context, method, _, service string, _ int, path string, _ url.Values, body []byte) ([]byte, int, error) {
	switch {
	case service == seaweed.FilerService && path == seaweed.FilerConfPath && method == http.MethodGet:
		if d.conf == nil {
			return nil, http.StatusNotFound, nil
		}
		return d.conf, http.StatusOK, nil
	case service == seaweed.FilerService && path == seaweed.FilerConfPath && method == http.MethodPut:
		d.conf = append([]byte(nil), body...)
		d.confPuts++
		return nil, http.StatusCreated, nil
	case service == seaweed.MasterService && path == "/vol/status":
		return d.volStatus(), http.StatusOK, nil
	}
	return nil, http.StatusNotFound, nil
}

// volStatus renders the volumes in the master's listing shape; "001"
// lists as {"node":1}, "000" as an empty placement (measured on the pin).
func (d *replicationDoer) volStatus() []byte {
	nodes := map[string][]map[string]any{}
	for _, vol := range d.volumes {
		placement := map[string]int{}
		if vol.Code == "001" {
			placement["node"] = 1
		}
		nodes[vol.Node] = append(nodes[vol.Node], map[string]any{
			"Id": vol.ID, "Collection": "b-files-1", "ReplicaPlacement": placement,
		})
	}
	data, err := json.Marshal(map[string]any{"Volumes": map[string]any{
		"DataCenters": map[string]any{"dc": map[string]any{"rack": nodes}},
	}})
	if err != nil {
		panic(err)
	}
	return data
}

func (d *replicationDoer) ExecInPod(_ context.Context, _, _, _ string, command []string) (string, error) {
	script := command[len(command)-1]
	d.scripts = append(d.scripts, script)
	if d.failures > 0 {
		d.failures--
		return "", errors.New("exec: filer pod not ready")
	}
	if match := replicationArg.FindStringSubmatch(script); match != nil {
		for i := range d.volumes {
			if !d.stuck[d.volumes[i].ID] {
				d.volumes[i].Code = match[1]
			}
		}
	}
	return d.output, nil
}

func (d *replicationDoer) ServiceAddress(context.Context, string, string, int) (string, error) {
	return "127.0.0.1:0", nil
}

func (d *replicationDoer) ForgetServiceAddress(string, string, int) {}

// seedConf writes a filer.conf whose buckets entry carries the code, the
// state a previous process leaves behind when it got that far.
func (d *replicationDoer) seedConf(t *testing.T, code string) {
	t.Helper()
	data, err := json.Marshal(seaweed.FilerConf{Locations: []seaweed.PathConf{
		{LocationPrefix: seaweed.BucketsPrefix, Replication: code},
	}})
	require.NoError(t, err)
	d.conf = data
}

func (d *replicationDoer) codes() []string {
	codes := make([]string, 0, len(d.volumes))
	for _, vol := range d.volumes {
		codes = append(codes, vol.Code)
	}
	return codes
}

// newReplicationController wires a managed controller to the fake
// transport with the admin channel already pointed at the filers, the
// state ensureObjectStore reaches before it converges replication.
func newReplicationController(doer *replicationDoer) *Controller {
	client := seaweed.NewClient(doer, Namespace)
	client.SetFilerTarget("app="+seaweed.FilerService, "filer")
	return &Controller{cfg: Config{Managed: true}, deps: Deps{Seaweed: client}}
}

// grownRow is the recorded shape after a second capable node joined.
func grownRow() store.ObjectStore {
	return store.ObjectStore{Name: seaweed.StoreName, Masters: 1, VolumeServers: 2, Replication: "001",
		Image: seaweed.Image, State: dbstore.StateActive}
}

// TestReconcileReplicationRetriesAfterFailure: the regression for a move
// whose first attempt fails after the shape is recorded. The failure is
// an error (a rate-limited requeue), the recorded shape stays as it is,
// and the next pass on the same process runs the move again and
// succeeds; once every volume carries the code nothing more runs.
func TestReconcileReplicationRetriesAfterFailure(t *testing.T) {
	t.Parallel()
	doer := &replicationDoer{
		volumes:  []fakeVolume{{ID: 1, Node: "node-a:8080", Code: "000"}, {ID: 2, Node: "node-a:8080", Code: "000"}},
		failures: 1,
	}
	controller := newReplicationController(doer)
	ctx := context.Background()
	row := grownRow()

	err := controller.reconcileReplication(ctx, row)
	require.Error(t, err)
	require.Contains(t, err.Error(), "configure volume replication")
	require.Equal(t, 1, doer.confPuts, "the path entry is written before the move")
	require.Len(t, doer.scripts, 1)
	require.Equal(t, "001", row.Replication, "the recorded shape is untouched by the failure")
	require.Equal(t, []string{"000", "000"}, doer.codes())

	require.NoError(t, controller.reconcileReplication(ctx, row))
	require.Len(t, doer.scripts, 2, "the unchanged shape still drives the move")
	require.Equal(t, []string{"001", "001"}, doer.codes())
	require.Equal(t, 1, doer.confPuts)

	require.NoError(t, controller.reconcileReplication(ctx, row))
	require.Len(t, doer.scripts, 2, "a converged store runs nothing")
	require.Equal(t, 1, doer.confPuts)
}

// TestReconcileReplicationRecoversAfterRestart: a process that wrote the
// path entry and then died before moving the volumes leaves a store
// whose recorded shape and filer document already read as grown. A fresh
// controller finds the volumes still on the old code and moves them
// without rewriting the document.
func TestReconcileReplicationRecoversAfterRestart(t *testing.T) {
	t.Parallel()
	doer := &replicationDoer{
		volumes: []fakeVolume{{ID: 1, Node: "node-a:8080", Code: "000"}, {ID: 2, Node: "node-a:8080", Code: "000"}},
	}
	doer.seedConf(t, "001")
	controller := newReplicationController(doer)

	require.NoError(t, controller.reconcileReplication(context.Background(), grownRow()))
	require.Equal(t, 0, doer.confPuts, "the document already carried the code")
	require.Len(t, doer.scripts, 1)
	require.Equal(t, []string{"001", "001"}, doer.codes())
}

// TestReconcileReplicationFinishesPartialMove: volume.configure.replication
// stops at the first volume server that refuses, so a pass can move some
// volumes and leave others. The listing shows which are left, and the
// next pass moves them; a fully moved store is silent.
func TestReconcileReplicationFinishesPartialMove(t *testing.T) {
	t.Parallel()
	doer := &replicationDoer{
		volumes: []fakeVolume{{ID: 1, Node: "node-a:8080", Code: "000"}, {ID: 2, Node: "node-b:8080", Code: "000"}},
		stuck:   map[int64]bool{2: true},
	}
	doer.seedConf(t, "001")
	controller := newReplicationController(doer)
	ctx := context.Background()
	row := grownRow()

	require.NoError(t, controller.reconcileReplication(ctx, row))
	require.Len(t, doer.scripts, 1)
	require.Equal(t, []string{"001", "000"}, doer.codes())

	doer.stuck = nil
	require.NoError(t, controller.reconcileReplication(ctx, row))
	require.Len(t, doer.scripts, 2, "the volume left behind drives another move")
	require.Equal(t, []string{"001", "001"}, doer.codes())

	require.NoError(t, controller.reconcileReplication(ctx, row))
	require.Len(t, doer.scripts, 2)
}

// TestReconcileReplicationRepairsPathDrift: a filer document whose
// buckets entry disagrees with the recorded code is rewritten even when
// every volume already carries the code, and the move is not run.
func TestReconcileReplicationRepairsPathDrift(t *testing.T) {
	t.Parallel()
	doer := &replicationDoer{
		volumes: []fakeVolume{{ID: 1, Node: "node-a:8080", Code: "001"}, {ID: 1, Node: "node-b:8080", Code: "001"}},
	}
	doer.seedConf(t, "000")
	controller := newReplicationController(doer)

	require.NoError(t, controller.reconcileReplication(context.Background(), grownRow()))
	require.Equal(t, 1, doer.confPuts)
	var conf seaweed.FilerConf
	require.NoError(t, json.Unmarshal(doer.conf, &conf))
	require.Equal(t, "001", conf.Find(seaweed.BucketsPrefix).Replication)
	require.Empty(t, doer.scripts, "nothing to move")
}

// TestReconcileReplicationSurfacesShellErrors: weed shell exits zero when
// a command fails, so the transcript's error line is the only signal; it
// becomes the pass's error and the volumes stay where they were.
func TestReconcileReplicationSurfacesShellErrors(t *testing.T) {
	t.Parallel()
	doer := &replicationDoer{
		volumes: []fakeVolume{{ID: 1, Node: "node-a:8080", Code: "000"}},
		stuck:   map[int64]bool{1: true},
		output:  "> lock\n> volume.configure.replication -replication=001 -collectionPattern=*\nerror: rpc error: volume server unavailable\n",
	}
	doer.seedConf(t, "001")
	controller := newReplicationController(doer)

	err := controller.reconcileReplication(context.Background(), grownRow())
	require.Error(t, err)
	require.Contains(t, err.Error(), "volume server unavailable")
	require.Equal(t, []string{"000"}, doer.codes())
}

// TestReconcileReplicationNeedsClient: without an admin client there is
// nothing to observe or move; the pass is a no-op rather than an error.
func TestReconcileReplicationNeedsClient(t *testing.T) {
	t.Parallel()
	controller := &Controller{cfg: Config{Managed: true}}
	require.NoError(t, controller.reconcileReplication(context.Background(), grownRow()))
}

// TestReconcileShapeGrowsWithoutSeaweed: growing the recorded shape is a
// database step alone. A second capable node turns the recorded
// replication on whether or not an admin client exists; the live row
// reads the grown shape and a further pass changes nothing.
func TestReconcileShapeGrowsWithoutSeaweed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dbSvc := dbstore.New(store.NewStore(testdb.New(t)))
	row, err := dbSvc.CreateObjectStore(ctx, dbstore.StoreInput{
		Name: seaweed.StoreName, Masters: 1, VolumeServers: 1, Replication: "000", Image: seaweed.Image,
	})
	require.NoError(t, err)

	observed := observe.NewStore(nil)
	observed.SetNodeCapabilities("node-a", []string{layout.CapabilityObjectStorage})
	observed.SetNodeCapabilities("node-b", []string{layout.CapabilityObjectStorage})
	controller := &Controller{cfg: Config{Managed: true}, deps: Deps{DB: dbSvc, Observed: observed}}

	require.NoError(t, controller.reconcileShape(ctx, row))
	require.Equal(t, "001", row.Replication)
	require.EqualValues(t, 2, row.VolumeServers)
	live, err := dbSvc.LiveObjectStore(ctx)
	require.NoError(t, err)
	require.Equal(t, "001", live.Replication)
	require.EqualValues(t, 2, live.VolumeServers)
	require.EqualValues(t, 1, live.Masters)

	require.NoError(t, controller.reconcileShape(ctx, live))
	again, err := dbSvc.LiveObjectStore(ctx)
	require.NoError(t, err)
	require.Equal(t, live.UpdatedAt, again.UpdatedAt, "an unchanged shape is not rewritten")
}
