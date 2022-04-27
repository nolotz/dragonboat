// Copyright 2017-2019 Lei Ni (nilei81@gmail.com) and other contributors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tan

import (
	"fmt"

	"github.com/cockroachdb/errors/oserror"

	"github.com/lni/dragonboat/v3/internal/fileutil"
	"github.com/lni/dragonboat/v3/raftio"
	"github.com/lni/vfs"
)

// dbKeeper keeps all tan db instances managed by a tan LogDB.
type dbKeeper interface {
	multiplexedLog() bool
	name(clusterID uint64, nodeID uint64) string
	key(clusterID uint64) uint64
	get(clusterID uint64, nodeID uint64) (*db, bool)
	set(clusterID uint64, nodeID uint64, db *db)
	iterate(f func(*db) error) error
}

var _ dbKeeper = (*regularKeeper)(nil)

// regularKeeper assigns a unique tan db instance to each raft node.
type regularKeeper struct {
	dbs map[raftio.NodeInfo]*db
}

func newRegularDBKeeper() *regularKeeper {
	return &regularKeeper{
		dbs: make(map[raftio.NodeInfo]*db),
	}
}

func (k *regularKeeper) multiplexedLog() bool {
	return false
}

func (k *regularKeeper) name(clusterID uint64, nodeID uint64) string {
	return fmt.Sprintf("node-%d-%d", clusterID, nodeID)
}

func (k *regularKeeper) key(clusterID uint64) uint64 {
	panic("not suppose to be called")
}

func (k *regularKeeper) get(clusterID uint64, nodeID uint64) (*db, bool) {
	ni := raftio.NodeInfo{ClusterID: clusterID, NodeID: nodeID}
	v, ok := k.dbs[ni]
	return v, ok
}

func (k *regularKeeper) set(clusterID uint64, nodeID uint64, db *db) {
	ni := raftio.NodeInfo{ClusterID: clusterID, NodeID: nodeID}
	k.dbs[ni] = db
}

func (k *regularKeeper) iterate(f func(*db) error) error {
	for _, db := range k.dbs {
		if err := f(db); err != nil {
			return err
		}
	}
	return nil
}

var _ dbKeeper = (*multiplexedKeeper)(nil)

// multiplexedKeeper divide all raft nodes into groups and assign nodes within
// the same group to a unique tan db instance. Each raft node is assigned to
// such a group by a so called key value.
type multiplexedKeeper struct {
	dbs map[uint64]*db
}

func newMultiplexedDBKeeper() *multiplexedKeeper {
	return &multiplexedKeeper{dbs: make(map[uint64]*db)}
}

func (k *multiplexedKeeper) multiplexedLog() bool {
	return true
}

func (k *multiplexedKeeper) name(clusterID uint64, nodeID uint64) string {
	return fmt.Sprintf("shard-%d", k.key(clusterID))
}

func (k *multiplexedKeeper) key(clusterID uint64) uint64 {
	return clusterID % 16
}

func (k *multiplexedKeeper) get(clusterID uint64, nodeID uint64) (*db, bool) {
	v, ok := k.dbs[k.key(clusterID)]
	return v, ok
}

func (k *multiplexedKeeper) set(clusterID uint64, nodeID uint64, db *db) {
	k.dbs[k.key(clusterID)] = db
}

func (k *multiplexedKeeper) iterate(f func(*db) error) error {
	for _, db := range k.dbs {
		if err := f(db); err != nil {
			return err
		}
	}
	return nil
}

// collection owns a collection of tan db instances.
type collection struct {
	fs      vfs.FS
	dirname string
	keeper  dbKeeper
}

func newCollection(dirname string, fs vfs.FS, regular bool) collection {
	var k dbKeeper
	if regular {
		k = newRegularDBKeeper()
	} else {
		k = newMultiplexedDBKeeper()
	}
	return collection{
		fs:      fs,
		dirname: dirname,
		keeper:  k,
	}
}

func (c *collection) multiplexedLog() bool {
	return c.keeper.multiplexedLog()
}

func (c *collection) key(clusterID uint64) uint64 {
	return c.keeper.key(clusterID)
}

func (c *collection) getDB(clusterID uint64, nodeID uint64) (*db, error) {
	db, ok := c.keeper.get(clusterID, nodeID)
	if ok {
		return db, nil
	}
	name := c.keeper.name(clusterID, nodeID)
	dbdir := c.fs.PathJoin(c.dirname, name)
	if err := c.prepareDir(dbdir); err != nil {
		return nil, err
	}
	db, err := open(dbdir, dbdir, &Options{FS: c.fs})
	if err != nil {
		return nil, err
	}
	c.keeper.set(clusterID, nodeID, db)
	return db, nil
}

func (c *collection) prepareDir(dbdir string) error {
	if _, err := c.fs.Stat(dbdir); oserror.IsNotExist(err) {
		if err := fileutil.MkdirAll(dbdir, c.fs); err != nil {
			return err
		}
	}
	return nil
}

func (c *collection) iterate(f func(*db) error) error {
	return c.keeper.iterate(f)
}
