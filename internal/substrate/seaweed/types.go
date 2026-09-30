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

// CollectionStat is one bucket's physical footprint, from the master's
// volume listing: logical bytes (volumes deduped by id, so replicas are not
// double-counted; quota charges the upload, not the fleet disk) including
// tombstoned-but-unvacuumed garbage.
type CollectionStat struct {
	SizeBytes int64
	FileCount int64
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
