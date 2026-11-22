package seaweed

import (
	"fmt"
	"slices"
	"strings"
)

// The wire shapes of the two single-writer documents the substrate
// reconciles (proto-JSON of seaweed's iam.S3ApiConfiguration and
// filer_pb.FilerConf, field names verified against the pin) plus the
// master's volume listing.

// IdentityConfig is the S3 identity document. Gateways subscribe to filer
// metadata changes and hot-reload it.
type IdentityConfig struct {
	Identities []Identity `json:"identities"`
}

// Identity is one S3 principal: a bucket service's keypair with actions
// scoped to exactly its bucket, or the inert bootstrap identity.
type Identity struct {
	Name        string       `json:"name"`
	Credentials []Credential `json:"credentials"`
	Actions     []string     `json:"actions"`
}

// Credential is an access keypair. Identities hold a list, the door to
// graceful add-new/revoke-old rotation later.
type Credential struct {
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
}

// Find returns the identity with the given name, or nil.
func (c *IdentityConfig) Find(name string) *Identity {
	for i := range c.Identities {
		if c.Identities[i].Name == name {
			return &c.Identities[i]
		}
	}
	return nil
}

// FilerConf is the per-path configuration document at FilerConfPath: the
// replication setting on the buckets prefix and per-bucket read-only quota
// flips. The substrate owns this document outright.
type FilerConf struct {
	Locations []PathConf `json:"locations"`
}

// PathConf configures one path prefix.
type PathConf struct {
	LocationPrefix string `json:"locationPrefix"`
	Replication    string `json:"replication,omitempty"`
	ReadOnly       bool   `json:"readOnly,omitempty"`
}

// Find returns the location entry for the prefix, or nil.
func (c *FilerConf) Find(prefix string) *PathConf {
	for i := range c.Locations {
		if c.Locations[i].LocationPrefix == prefix {
			return &c.Locations[i]
		}
	}
	return nil
}

// CollectionStat is one bucket's footprint from the master's volume
// listing, volumes deduped by id so replicas are not double-counted
// (quota charges the upload, not the fleet disk). SizeBytes is what the
// volumes hold on disk, deleted-but-unvacuumed entries included; LiveBytes
// subtracts the deleted bytes the volume servers already account for
// (measured on the pin: a delete raises DeletedByteCount on the next
// heartbeat, long before the vacuum reclaims the disk), so a quota
// judged on it opens again as soon as space is freed. EntryCount is the
// live needle count: large objects are chunked into several entries, so
// it is not an object count.
type CollectionStat struct {
	SizeBytes    int64
	LiveBytes    int64
	DeletedBytes int64
	EntryCount   int64
	// Volumes counts the collection's distinct volumes; Replicas counts
	// their copies, the volume slots the collection holds on the servers.
	Volumes  int
	Replicas int
}

// collectionStats folds one volume listing into per-collection footprints.
func collectionStats(vs volStatus) map[string]CollectionStat {
	seen := map[int64]bool{}
	stats := map[string]CollectionStat{}
	for _, dc := range vs.Volumes.DataCenters {
		for _, rack := range dc {
			for _, node := range rack {
				for _, vol := range node {
					if vol.Collection == "" {
						continue
					}
					s := stats[vol.Collection]
					s.Replicas++
					if seen[vol.ID] {
						stats[vol.Collection] = s
						continue
					}
					seen[vol.ID] = true
					s.Volumes++
					s.SizeBytes += vol.Size
					s.DeletedBytes += vol.DeletedByteCount
					s.LiveBytes += max(vol.Size-vol.DeletedByteCount, 0)
					s.EntryCount += max(vol.FileCount-vol.DeleteCount, 0)
					stats[vol.Collection] = s
				}
			}
		}
	}
	return stats
}

// volStatus is the master /vol/status answer, reduced to what we read.
type volStatus struct {
	Volumes struct {
		DataCenters map[string]map[string]map[string][]volumeInfo `json:"DataCenters"`
	} `json:"Volumes"`
}

type volumeInfo struct {
	ID               int64            `json:"Id"`
	Size             int64            `json:"Size"`
	Collection       string           `json:"Collection"`
	FileCount        int64            `json:"FileCount"`
	DeleteCount      int64            `json:"DeleteCount"`
	DeletedByteCount int64            `json:"DeletedByteCount"`
	ReplicaPlacement replicaPlacement `json:"ReplicaPlacement"`
}

// replicaPlacement is a volume's replication code decomposed the way the
// master reports it (measured on the pin: "001" lists as {"node":1}, one
// extra copy on another node of the same rack; the digits are data
// center, rack, node).
type replicaPlacement struct {
	SameRackCount       int `json:"node"`
	DiffRackCount       int `json:"rack"`
	DiffDataCenterCount int `json:"dc"`
}

// parseReplication decomposes a three-digit replication code (data
// center, rack, node) into a placement; anything else is malformed.
func parseReplication(code string) (replicaPlacement, error) {
	if len(code) != 3 {
		return replicaPlacement{}, fmt.Errorf("seaweed: replication code %q: want three digits", code)
	}
	digits := make([]int, 3)
	for i, r := range code {
		if r < '0' || r > '9' {
			return replicaPlacement{}, fmt.Errorf("seaweed: replication code %q: want three digits", code)
		}
		digits[i] = int(r - '0')
	}
	return replicaPlacement{DiffDataCenterCount: digits[0], DiffRackCount: digits[1], SameRackCount: digits[2]}, nil
}

// code renders the placement back into its replication code.
func (p replicaPlacement) code() string {
	return fmt.Sprintf("%d%d%d", p.DiffDataCenterCount, p.DiffRackCount, p.SameRackCount)
}

// copies is the number of copies the placement calls for.
func (p replicaPlacement) copies() int {
	return 1 + p.SameRackCount + p.DiffRackCount + p.DiffDataCenterCount
}

// VolumeHealth is the fleet-wide replica accounting from the master's
// volume listing, measured against the store's recorded replication: how
// many distinct volumes exist, how many have fewer copies than the
// recorded code calls for (a node lost, or copies the maintenance loop
// has not created yet), and how many still carry a different placement
// (a replication change the substrate has not applied to them yet; the
// maintenance loop only repairs a volume against its own placement, so
// these never gain their copy until they are moved).
type VolumeHealth struct {
	Volumes         int
	UnderReplicated int
	Unconfigured    int
	// UnconfiguredIDs lists the volumes still on another placement,
	// ascending, for the operator log.
	UnconfiguredIDs []int64
}

// volumeHealth folds one volume listing into replica counts against the
// desired placement.
func volumeHealth(vs volStatus, desired replicaPlacement) VolumeHealth {
	copies := map[int64]int{}
	placements := map[int64]replicaPlacement{}
	for _, dc := range vs.Volumes.DataCenters {
		for _, rack := range dc {
			for _, node := range rack {
				for _, vol := range node {
					copies[vol.ID]++
					placements[vol.ID] = vol.ReplicaPlacement
				}
			}
		}
	}
	health := VolumeHealth{Volumes: len(copies)}
	wanted := desired.copies()
	for id, have := range copies {
		if have < wanted {
			health.UnderReplicated++
		}
		if placements[id] != desired {
			health.UnconfiguredIDs = append(health.UnconfiguredIDs, id)
		}
	}
	slices.Sort(health.UnconfiguredIDs)
	health.Unconfigured = len(health.UnconfiguredIDs)
	return health
}

// clusterStatus is the master /cluster/status answer, reduced to what the
// probe reads.
type clusterStatus struct {
	IsLeader bool     `json:"IsLeader"`
	Leader   string   `json:"Leader"`
	Peers    []string `json:"Peers"`
}

// dirStatus is the master /dir/status answer, reduced to the volume-server
// topology the probe counts and the slots each server offers.
type dirStatus struct {
	Topology struct {
		DataCenters []struct {
			Racks []struct {
				DataNodes []struct {
					URL     string `json:"Url"`
					Volumes int64  `json:"Volumes"`
					Max     int64  `json:"Max"`
				} `json:"DataNodes"`
			} `json:"Racks"`
		} `json:"DataCenters"`
	} `json:"Topology"`
}

// ServerSlots is one volume server's volume slots: how many volumes it
// may hold (Max, which a server started with -max=0 derives from its free
// disk divided by the volume size limit and recomputes as either changes)
// and how many it holds, replicas included.
type ServerSlots struct {
	URL     string
	Max     int
	Volumes int
}

// Free is the slots the server has left.
func (s ServerSlots) Free() int {
	return max(s.Max-s.Volumes, 0)
}

// SlotReport is the store's volume slot accounting from the master's
// directory topology, one entry per connected volume server.
type SlotReport struct {
	Servers []ServerSlots
}

// Total is every server's slot count summed.
func (r SlotReport) Total() int {
	total := 0
	for _, server := range r.Servers {
		total += server.Max
	}
	return total
}

// Used is every volume replica the servers hold.
func (r SlotReport) Used() int {
	used := 0
	for _, server := range r.Servers {
		used += server.Volumes
	}
	return used
}

// Free is the slots left across the servers.
func (r SlotReport) Free() int {
	free := 0
	for _, server := range r.Servers {
		free += server.Free()
	}
	return free
}

// VolumeHeadroom is how many more volumes the servers can grow when each
// needs a slot on `copies` different servers (the replication code's
// copy count): the largest n with sum(min(free_i, n)) >= copies * n, the
// condition under which n groups of distinct servers exist. A server
// cannot hold two copies of one volume, so a single server with plenty of
// room adds at most one copy per volume.
func (r SlotReport) VolumeHeadroom(copies int) int {
	copies = max(copies, 1)
	free := make([]int, 0, len(r.Servers))
	total := 0
	for _, server := range r.Servers {
		if f := server.Free(); f > 0 {
			free = append(free, f)
			total += f
		}
	}
	if len(free) < copies {
		return 0
	}
	// The condition is monotone in n; search the largest n that meets it.
	lo, hi := 0, total/copies
	for lo < hi {
		n := (lo + hi + 1) / 2
		sum := 0
		for _, f := range free {
			sum += min(f, n)
		}
		if sum >= copies*n {
			lo = n
		} else {
			hi = n - 1
		}
	}
	return lo
}

// BucketHeadroom is how many more buckets the store can take before a new
// bucket's first write finds no room: each new bucket is a collection
// whose first write grows volumesPerBucket volumes (VolumesPerBucket),
// each with its replication's copies on different servers. An estimate:
// existing buckets take slots as they fill their volumes, and a server's
// slot count follows its free disk.
func (r SlotReport) BucketHeadroom(replication string, volumesPerBucket int) int {
	return r.VolumeHeadroom(ReplicationCopies(replication)) / max(volumesPerBucket, 1)
}

// ReplicationCopies is how many copies a replication code keeps of every
// volume; a malformed code counts as one.
func ReplicationCopies(code string) int {
	placement, err := parseReplication(code)
	if err != nil {
		return 1
	}
	return placement.copies()
}

// slotReport folds the directory topology into per-server slots.
func slotReport(ds dirStatus) SlotReport {
	report := SlotReport{Servers: []ServerSlots{}}
	for _, dc := range ds.Topology.DataCenters {
		for _, rack := range dc.Racks {
			for _, node := range rack.DataNodes {
				report.Servers = append(report.Servers, ServerSlots{
					URL: node.URL, Max: int(node.Max), Volumes: int(node.Volumes),
				})
			}
		}
	}
	slices.SortFunc(report.Servers, func(a, b ServerSlots) int { return strings.Compare(a.URL, b.URL) })
	return report
}
