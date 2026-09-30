package seaweed

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
}

// collectionStats folds one volume listing into per-collection footprints.
func collectionStats(vs volStatus) map[string]CollectionStat {
	seen := map[int64]bool{}
	stats := map[string]CollectionStat{}
	for _, dc := range vs.Volumes.DataCenters {
		for _, rack := range dc {
			for _, node := range rack {
				for _, vol := range node {
					if vol.Collection == "" || seen[vol.ID] {
						continue
					}
					seen[vol.ID] = true
					s := stats[vol.Collection]
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

// copies is the number of copies the placement calls for.
func (p replicaPlacement) copies() int {
	return 1 + p.SameRackCount + p.DiffRackCount + p.DiffDataCenterCount
}

// VolumeHealth is the fleet-wide replica accounting from the master's
// volume listing: how many distinct volumes exist and how many of them
// have fewer copies than their placement calls for (a node lost, or a
// replication change the maintenance loop has not caught up with).
type VolumeHealth struct {
	Volumes         int
	UnderReplicated int
}

// volumeHealth folds one volume listing into replica counts.
func volumeHealth(vs volStatus) VolumeHealth {
	copies := map[int64]int{}
	wanted := map[int64]int{}
	for _, dc := range vs.Volumes.DataCenters {
		for _, rack := range dc {
			for _, node := range rack {
				for _, vol := range node {
					copies[vol.ID]++
					wanted[vol.ID] = vol.ReplicaPlacement.copies()
				}
			}
		}
	}
	health := VolumeHealth{Volumes: len(copies)}
	for id, have := range copies {
		if have < wanted[id] {
			health.UnderReplicated++
		}
	}
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
// topology the probe counts.
type dirStatus struct {
	Topology struct {
		DataCenters []struct {
			Racks []struct {
				DataNodes []struct {
					URL string `json:"Url"`
				} `json:"DataNodes"`
			} `json:"Racks"`
		} `json:"DataCenters"`
	} `json:"Topology"`
}
