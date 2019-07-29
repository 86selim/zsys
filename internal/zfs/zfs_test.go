package zfs_test

import (
	"io/ioutil"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"syscall"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/k0kubun/pp"
	"github.com/ubuntu/zsys/internal/config"
	"github.com/ubuntu/zsys/internal/testutils"
	"github.com/ubuntu/zsys/internal/zfs"
)

func init() {
	testutils.InstallUpdateFlag()
	config.SetVerboseMode(1)
}

func TestScan(t *testing.T) {
	skipOnZFSPermissionDenied(t)

	tests := map[string]struct {
		def     string
		mounted string

		wantErr bool
	}{
		"One pool, N datasets, N children":                                         {def: "one_pool_n_datasets_n_children.yaml"},
		"One pool, N datasets, N children, N snapshots":                            {def: "one_pool_n_datasets_n_children_n_snapshots.yaml"},
		"One pool, N datasets, N children, N snapshots, intermediate canmount=off": {def: "one_pool_n_datasets_n_children_n_snapshots_canmount_off.yaml"},
		"One pool, one dataset":                                                    {def: "one_pool_one_dataset.yaml"},
		"One pool, one dataset, different mountpoints":                             {def: "one_pool_one_dataset_different_mountpoints.yaml"},
		"One pool, one dataset, no property":                                       {def: "one_pool_one_dataset_no_property.yaml"},
		"One pool, one dataset, with bootfsdatasets property":                      {def: "one_pool_one_dataset_with_bootfsdatasets.yaml"},
		"One pool, one dataset, with bootfsdatasets property, multiple elems":      {def: "one_pool_one_dataset_with_bootfsdatasets_multiple.yaml"},
		"One pool, N datasets":                                                     {def: "one_pool_n_datasets.yaml"},
		"One pool, one dataset, one snapshot":                                      {def: "one_pool_one_dataset_one_snapshot.yaml"},
		"One pool, one dataset, canmount=noauto":                                   {def: "one_pool_one_dataset_canmount_noauto.yaml"},
		"One pool, N datasets, one snapshot":                                       {def: "one_pool_n_datasets_one_snapshot.yaml"},
		"One pool non-root mpoint, N datasets no mountpoint":                       {def: "one_pool_with_nonroot_mountpoint_n_datasets_no_mountpoint.yaml"},
		"Two pools, N datasets":                                                    {def: "two_pools_n_datasets.yaml"},
		"Two pools, N datasets, N snapshots":                                       {def: "two_pools_n_datasets_n_snapshots.yaml"},
		"One mounted dataset":                                                      {def: "one_pool_n_datasets_n_children.yaml", mounted: "rpool/ROOT/ubuntu"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			dir, cleanup := tempDir(t)
			defer cleanup()

			ta := timeAsserter(time.Now())
			fPools := newFakePools(t, filepath.Join("testdata", tc.def))
			defer fPools.create(dir)()

			if tc.mounted != "" {
				temp := filepath.Join(dir, "tempmount")
				if err := os.MkdirAll(temp, 0755); err != nil {
					t.Fatalf("couldn't create temporary mount point directory %q: %v", temp, err)
				}
				// zfs will unmount it when exporting the pool
				if err := syscall.Mount(tc.mounted, temp, "zfs", 0, "zfsutil"); err != nil {
					t.Fatalf("couldn't prepare and mount %q: %v", tc.mounted, err)
				}
			}

			z := zfs.New()
			got, err := z.Scan()
			if err != nil {
				if !tc.wantErr {
					t.Fatalf("expected no error but got: %v", err)
				}
				return
			}
			if tc.wantErr {
				t.Fatal("expected an error but got none")
			}

			assertDatasetsToGolden(t, ta, got, false)
		})
	}
}

func TestSnapshot(t *testing.T) {
	skipOnZFSPermissionDenied(t)

	tests := map[string]struct {
		def          string
		snapshotName string
		datasetName  string
		recursive    bool

		wantErr bool
		isNoOp  bool
	}{
		"Simple snapshot":                                      {def: "one_pool_one_dataset.yaml", snapshotName: "snap1", datasetName: "rpool"},
		"Simple snapshot with children":                        {def: "layout1__one_pool_n_datasets.yaml", snapshotName: "snap1", datasetName: "rpool/ROOT/ubuntu_1234"},
		"Simple snapshot even if on subdataset already exists": {def: "layout1__one_pool_n_datasets_n_snapshots.yaml", snapshotName: "snap_r1", datasetName: "rpool/ROOT"},

		"Recursive snapshots":                         {def: "layout1__one_pool_n_datasets.yaml", snapshotName: "snap1", datasetName: "rpool/ROOT/ubuntu_1234", recursive: true},
		"Recursive snapshot on leaf dataset":          {def: "one_pool_one_dataset.yaml", snapshotName: "snap1", datasetName: "rpool", recursive: true},
		"Recursive snapshots alongside existing ones": {def: "layout1__one_pool_n_datasets_n_snapshots.yaml", snapshotName: "snap1", datasetName: "rpool/ROOT/ubuntu_1234", recursive: true},

		"Dataset doesn't exist":                             {def: "one_pool_one_dataset.yaml", snapshotName: "snap1", datasetName: "doesntexit", wantErr: true, isNoOp: true},
		"Invalid snapshot name":                             {def: "one_pool_one_dataset.yaml", snapshotName: "", datasetName: "rpool", wantErr: true, isNoOp: true},
		"Snapshot on dataset already exists":                {def: "layout1__one_pool_n_datasets_n_snapshots.yaml", snapshotName: "snap_r1", datasetName: "rpool/ROOT/ubuntu_1234/opt", wantErr: true, isNoOp: true},
		"Snapshot on subdataset already exists":             {def: "layout1__one_pool_n_datasets_n_snapshots.yaml", snapshotName: "snap_r1", datasetName: "rpool/ROOT", recursive: true, wantErr: true, isNoOp: true},
		"Snapshot on dataset exists, but not on subdataset": {def: "layout1_missing_intermediate_snapshot.yaml", snapshotName: "snap_r1", datasetName: "rpool/ROOT/ubuntu_1234", wantErr: true, isNoOp: true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			dir, cleanup := tempDir(t)
			defer cleanup()

			ta := timeAsserter(time.Now())
			fPools := newFakePools(t, filepath.Join("testdata", tc.def))
			defer fPools.create(dir)()
			z := zfs.New()
			// Scan initial state for no-op
			var initState []zfs.Dataset
			if tc.isNoOp {
				var err error
				initState, err = z.Scan()
				if err != nil {
					t.Fatalf("couldn't get initial state: %v", err)
				}
			}

			err := z.Snapshot(tc.snapshotName, tc.datasetName, tc.recursive)

			if err != nil {
				if !tc.wantErr {
					t.Fatalf("expected no error but got: %v", err)
				}
				// we don't return because we want to check that on error, Snapshot() is a no-op
			}
			if err == nil && tc.wantErr {
				t.Fatal("expected an error but got none")
			}

			got, err := z.Scan()
			if err != nil {
				t.Fatalf("couldn't get final state: %v", err)
			}

			if tc.isNoOp {
				assertDatasetsEquals(t, ta, initState, got, true)
				return
			}
			assertDatasetsToGolden(t, ta, got, true)
		})
	}
}

func TestClone(t *testing.T) {
	skipOnZFSPermissionDenied(t)

	tests := map[string]struct {
		def        string
		dataset    string
		suffix     string
		skipBootfs bool
		recursive  bool

		wantErr bool
		isNoOp  bool
	}{
		// TODO: Test case with user properties changed between snapshot and parent (with children inheriting)
		"Simple clone":    {def: "layout1__one_pool_n_datasets_n_snapshots.yaml", dataset: "rpool/ROOT/ubuntu_1234@snap_r1", suffix: "5678"},
		"Recursive clone": {def: "layout1__one_pool_n_datasets_n_snapshots.yaml", dataset: "rpool/ROOT/ubuntu_1234@snap_r1", suffix: "5678", recursive: true},
		"Simple clone ignore missing intermediate snapshots": {def: "layout1_missing_intermediate_snapshot.yaml", dataset: "rpool/ROOT/ubuntu_1234@snap_r1", suffix: "5678"},

		"Simple clone keeps canmount off as off":                      {def: "one_pool_n_datasets_one_snapshot_with_canmount_off.yaml", dataset: "rpool/ROOT/ubuntu@snap1", suffix: "5678"},
		"Simple clone keeps canmount noauto as noauto":                {def: "one_pool_n_datasets_one_snapshot_with_canmount_noauto.yaml", dataset: "rpool/ROOT/ubuntu@snap1", suffix: "5678"},
		"Simple clone set canmount on to noauto":                      {def: "one_pool_n_datasets_one_snapshot.yaml", dataset: "rpool/ROOT/ubuntu@snap1", suffix: "5678"},
		"Simple clone on non root local mountpoint keeps mountpoints": {def: "one_pool_n_datasets_one_snapshot_non_root.yaml", dataset: "rpool/ROOT/ubuntu@snap1", suffix: "5678"},

		"Simple clone on dataset without suffix":    {def: "layout1__one_pool_n_datasets_n_snapshots_without_suffix.yaml", dataset: "rpool/ROOT/ubuntu@snap_r1", suffix: "5678"},
		"Recursive clone on dataset without suffix": {def: "layout1__one_pool_n_datasets_n_snapshots_without_suffix.yaml", dataset: "rpool/ROOT/ubuntu@snap_r1", suffix: "5678", recursive: true},

		"Recursive missing some leaf snapshots":    {def: "layout1_missing_leaf_snapshot.yaml", dataset: "rpool/ROOT/ubuntu_1234@snap_r1", suffix: "5678", recursive: true},
		"Recursive missing intermediate snapshots": {def: "layout1_missing_intermediate_snapshot.yaml", dataset: "rpool/ROOT/ubuntu_1234@snap_r1", suffix: "5678", recursive: true, wantErr: true, isNoOp: true},

		"Allow cloning ignoring zsys bootfs": {def: "layout1_with_bootfs_already_cloned.yaml", dataset: "rpool/ROOT/ubuntu_1234@snap_r1", suffix: "5678", skipBootfs: true, recursive: true},

		"Snapshot doesn't exists":         {def: "layout1__one_pool_n_datasets_n_snapshots.yaml", dataset: "rpool/ROOT/ubuntu_1234@doesntexists", suffix: "5678", wantErr: true, isNoOp: true},
		"Dataset doesn't exists":          {def: "layout1__one_pool_n_datasets_n_snapshots.yaml", dataset: "rpool/ROOT/ubuntu_doesntexist@something", suffix: "5678", wantErr: true, isNoOp: true},
		"No suffix provided":              {def: "layout1__one_pool_n_datasets_n_snapshots.yaml", dataset: "rpool/ROOT/ubuntu_1234@snap_r1", wantErr: true, isNoOp: true},
		"Suffixed dataset already exists": {def: "layout1_with_bootfs_already_cloned.yaml", dataset: "rpool/ROOT/ubuntu_1234@snap_r1", suffix: "5678", wantErr: true, isNoOp: true},
		"Clone on root fails":             {def: "one_pool_one_dataset_one_snapshot.yaml", dataset: "rpool@snap1", suffix: "5678", wantErr: true, isNoOp: true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			dir, cleanup := tempDir(t)
			defer cleanup()

			ta := timeAsserter(time.Now())
			fPools := newFakePools(t, filepath.Join("testdata", tc.def))
			defer fPools.create(dir)()
			z := zfs.New()
			// Scan initial state for no-op
			var initState []zfs.Dataset
			if tc.isNoOp {
				var err error
				initState, err = z.Scan()
				if err != nil {
					t.Fatalf("couldn't get initial state: %v", err)
				}
			}

			err := z.Clone(tc.dataset, tc.suffix, tc.skipBootfs, tc.recursive)

			if err != nil {
				if !tc.wantErr {
					t.Fatalf("expected no error but got: %v", err)
				}
				// we don't return because we want to check that on error, Clone() is a no-op
			}
			if err == nil && tc.wantErr {
				t.Fatal("expected an error but got none")
			}

			got, err := z.Scan()
			if err != nil {
				t.Fatalf("couldn't get final state: %v", err)
			}

			if tc.isNoOp {
				assertDatasetsEquals(t, ta, initState, got, true)
				return
			}
			assertDatasetsToGolden(t, ta, got, true)
		})
	}
}

func TestPromote(t *testing.T) {
	skipOnZFSPermissionDenied(t)

	tests := map[string]struct {
		def     string
		dataset string

		// prepare cloning/promotion scenarios
		cloneFrom       string
		cloneOnlyOne    bool   // only clone root element to have misssing intermediate snapshots
		alreadyPromoted string // pre-promote a dataset and its children

		wantErr bool
		isNoOp  bool
	}{
		"Promote with snapshots on origin":    {def: "layout1__one_pool_n_datasets_n_snapshots.yaml", dataset: "rpool/ROOT/ubuntu_5678", cloneFrom: "rpool/ROOT/ubuntu_1234@snap_r1"},
		"Promote missing some leaf snapshots": {def: "layout1_missing_leaf_snapshot.yaml", dataset: "rpool/ROOT/ubuntu_5678", cloneFrom: "rpool/ROOT/ubuntu_1234@snap_r1"},

		"Promote already promoted hierarchy":  {def: "layout1__one_pool_n_datasets_n_snapshots.yaml", dataset: "rpool/ROOT/ubuntu_1234", isNoOp: true},
		"Root of hierarchy already promoted":  {def: "layout1__one_pool_n_datasets_n_snapshots.yaml", dataset: "rpool/ROOT/ubuntu_5678", cloneFrom: "rpool/ROOT/ubuntu_1234@snap_r1", alreadyPromoted: "rpool/ROOT/ubuntu_5678"},
		"Child of hierarchy already promoted": {def: "layout1__one_pool_n_datasets_n_snapshots.yaml", dataset: "rpool/ROOT/ubuntu_5678", cloneFrom: "rpool/ROOT/ubuntu_1234@snap_r1", alreadyPromoted: "rpool/ROOT/ubuntu_5678/var"},

		"Dataset doesn't exists":                            {def: "layout1__one_pool_n_datasets_n_snapshots.yaml", dataset: "rpool/ROOT/ubuntu_doesntexist", wantErr: true, isNoOp: true},
		"Promote a snapshot fails":                          {def: "layout1__one_pool_n_datasets_n_snapshots.yaml", dataset: "rpool/ROOT/ubuntu_1234@snap_r1", wantErr: true, isNoOp: true},
		"Can't promote when missing intermediate snapshots": {def: "layout1_missing_intermediate_snapshot.yaml", dataset: "rpool/ROOT/ubuntu_5678", cloneFrom: "rpool/ROOT/ubuntu_1234@snap_r1", cloneOnlyOne: true, wantErr: true, isNoOp: true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			dir, cleanup := tempDir(t)
			defer cleanup()

			ta := timeAsserter(time.Now())
			fPools := newFakePools(t, filepath.Join("testdata", tc.def))
			defer fPools.create(dir)()
			z := zfs.New()
			if tc.cloneFrom != "" {
				err := z.Clone(tc.cloneFrom, "5678", false, !tc.cloneOnlyOne)
				if err != nil {
					t.Fatalf("couldn't setup testbed when cloning: %v", err)
				}
			}
			if tc.alreadyPromoted != "" {
				err := z.Promote(tc.alreadyPromoted)
				if err != nil {
					t.Fatalf("couldn't setup testbed when prepromoting %q: %v", tc.alreadyPromoted, err)
				}
			}
			// Scan initial state for no-op
			var initState []zfs.Dataset
			if tc.isNoOp {
				var err error
				initState, err = z.Scan()
				if err != nil {
					t.Fatalf("couldn't get initial state: %v", err)
				}
			}

			err := z.Promote(tc.dataset)

			if err != nil {
				if !tc.wantErr {
					t.Fatalf("expected no error but got: %v", err)
				}
				// we don't return because we want to check that on error, Clone() is a no-op
			}
			if err == nil && tc.wantErr {
				t.Fatal("expected an error but got none")
			}

			got, err := z.Scan()
			if err != nil {
				t.Fatalf("couldn't get final state: %v", err)
			}

			if tc.isNoOp {
				assertDatasetsEquals(t, ta, initState, got, true)
				return
			}
			assertDatasetsToGolden(t, ta, got, true)
		})
	}
}

func TestDestroy(t *testing.T) {
	skipOnZFSPermissionDenied(t)

	tests := map[string]struct {
		def     string
		dataset string

		// prepare cloning/promotion scenarios
		cloneFrom       string
		alreadyPromoted string // pre-promote a dataset and its children

		wantErr bool
		isNoOp  bool
	}{
		"Leaf simple":                    {def: "layout1__one_pool_n_datasets.yaml", dataset: "rpool/ROOT/ubuntu_1234/var/lib/apt"},
		"Hierarchy simple":               {def: "layout1__one_pool_n_datasets.yaml", dataset: "rpool/ROOT/ubuntu_1234"},
		"Hierarchy with snapshots":       {def: "layout1__one_pool_n_datasets_n_snapshots.yaml", dataset: "rpool/ROOT/ubuntu_1234"},
		"Hierarchy with promoted clones": {def: "layout1__one_pool_n_datasets_n_snapshots.yaml", dataset: "rpool/ROOT/ubuntu_1234", cloneFrom: "rpool/ROOT/ubuntu_1234@snap_r1", alreadyPromoted: "rpool/ROOT/ubuntu_5678"},

		"Leaf snapshot simple": {def: "layout1__one_pool_n_datasets_n_snapshots.yaml", dataset: "rpool/ROOT/ubuntu_1234/var/lib/apt@snap_r1"},
		"Hierarchy snapshot":   {def: "layout1__one_pool_n_datasets_n_snapshots.yaml", dataset: "rpool/ROOT/ubuntu_1234@snap_r1"},

		"Dataset doesn't exists":                    {def: "layout1__one_pool_n_datasets_n_snapshots.yaml", dataset: "rpool/ROOT/ubuntu_doesntexist", wantErr: true, isNoOp: true},
		"Hierarchy with unpromoted clones":          {def: "layout1__one_pool_n_datasets_n_snapshots.yaml", dataset: "rpool/ROOT/ubuntu_1234", cloneFrom: "rpool/ROOT/ubuntu_1234@snap_r1", wantErr: true, isNoOp: true},
		"Hierarchy with unpromoted clones non root": {def: "layout1__one_pool_n_datasets_n_snapshots.yaml", dataset: "rpool/ROOT/ubuntu_1234", cloneFrom: "rpool/ROOT/ubuntu_1234/var@snap_r1", wantErr: true, isNoOp: true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			dir, cleanup := tempDir(t)
			defer cleanup()

			ta := timeAsserter(time.Now())
			fPools := newFakePools(t, filepath.Join("testdata", tc.def))
			defer fPools.create(dir)()
			z := zfs.New()
			if tc.cloneFrom != "" {
				err := z.Clone(tc.cloneFrom, "5678", false, true)
				if err != nil {
					t.Fatalf("couldn't setup testbed when cloning: %v", err)
				}
			}
			if tc.alreadyPromoted != "" {
				err := z.Promote(tc.alreadyPromoted)
				if err != nil {
					t.Fatalf("couldn't setup testbed when prepromoting %q: %v", tc.alreadyPromoted, err)
				}
			}
			// Scan initial state for no-op
			var initState []zfs.Dataset
			if tc.isNoOp {
				var err error
				initState, err = z.Scan()
				if err != nil {
					t.Fatalf("couldn't get initial state: %v", err)
				}
			}

			err := z.Destroy(tc.dataset)

			if err != nil {
				if !tc.wantErr {
					t.Fatalf("expected no error but got: %v", err)
				}
				// we don't return because we want to check that on error, Clone() is a no-op
			}
			if err == nil && tc.wantErr {
				t.Fatal("expected an error but got none")
			}

			got, err := z.Scan()
			if err != nil {
				t.Fatalf("couldn't get final state: %v", err)
			}

			if tc.isNoOp {
				assertDatasetsEquals(t, ta, initState, got, true)
				return
			}
			assertDatasetsToGolden(t, ta, got, true)
		})
	}
}

func TestSetProperty(t *testing.T) {
	skipOnZFSPermissionDenied(t)

	tests := map[string]struct {
		def           string
		propertyName  string
		propertyValue string
		dataset       string
		force         bool

		wantErr bool
		isNoOp  bool
	}{
		"User property (local)":       {def: "one_pool_one_dataset_with_bootfsdatasets.yaml", propertyName: zfs.BootfsDatasetsProp, propertyValue: "SetProperty Value", dataset: "rpool"},
		"Authorized property (local)": {def: "one_pool_one_dataset_with_bootfsdatasets.yaml", propertyName: zfs.CanmountProp, propertyValue: "noauto", dataset: "rpool"},
		"User property (none)":        {def: "one_pool_one_dataset_with_bootfsdatasets.yaml", propertyName: zfs.BootfsDatasetsProp, propertyValue: "SetProperty Value", dataset: "rpool"},
		// There is no authorized properties that can be "none" for now

		"User property (inherit)":            {def: "one_pool_n_datasets_n_children_with_bootfsdatasets.yaml", propertyName: zfs.BootfsDatasetsProp, propertyValue: "SetProperty Value", dataset: "rpool/ROOT/ubuntu/var"},
		"User property (inherit but forced)": {def: "one_pool_n_datasets_n_children_with_bootfsdatasets.yaml", propertyName: zfs.BootfsDatasetsProp, propertyValue: "SetProperty Value", dataset: "rpool/ROOT/ubuntu/var", force: true},
		// There is no authorized properties that can be inherited

		"Property on snapshot (parent local)":                   {def: "one_pool_one_dataset_one_snapshot_with_bootfsdatasets.yaml", propertyName: zfs.BootfsDatasetsProp, propertyValue: "SetProperty Value", dataset: "rpool@snap1", wantErr: true, isNoOp: true},
		"Property on snapshot (parent none)":                    {def: "one_pool_one_dataset_one_snapshot.yaml", propertyName: zfs.BootfsDatasetsProp, propertyValue: "SetProperty Value", dataset: "rpool@snap1", wantErr: true, isNoOp: true},
		"Property on snapshot (parent inherit)":                 {def: "one_pool_n_datasets_n_children_n_snapshots_with_bootfsdatasets.yaml", propertyName: zfs.BootfsDatasetsProp, propertyValue: "SetProperty Value", dataset: "rpool/ROOT/ubuntu/var@snap_v1", wantErr: true, isNoOp: true},
		"User property on snapshot (parent inherit but forced)": {def: "one_pool_n_datasets_n_children_n_snapshots_with_bootfsdatasets.yaml", propertyName: zfs.BootfsDatasetsProp, propertyValue: "SetProperty Value", dataset: "rpool/ROOT/ubuntu/var@snap_v1", wantErr: true, isNoOp: true, force: true},

		"Unauthorized property":                          {def: "one_pool_one_dataset.yaml", propertyName: "mountpoint", propertyValue: "/setproperty/value", dataset: "rpool", wantErr: true, isNoOp: true},
		"Dataset doesn't exists":                         {def: "one_pool_one_dataset.yaml", propertyName: zfs.BootfsDatasetsProp, propertyValue: "SetProperty Value", dataset: "rpool10", wantErr: true, isNoOp: true},
		"Authorized property on snapshot doesn't exists": {def: "one_pool_one_dataset_one_snapshot.yaml", propertyName: zfs.CanmountProp, propertyValue: "yes", dataset: "rpool@snap1", wantErr: true, isNoOp: true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			dir, cleanup := tempDir(t)
			defer cleanup()

			ta := timeAsserter(time.Now())
			fPools := newFakePools(t, filepath.Join("testdata", tc.def))
			defer fPools.create(dir)()
			z := zfs.New()
			// Scan initial state for no-op
			var initState []zfs.Dataset
			if tc.isNoOp {
				var err error
				initState, err = z.Scan()
				if err != nil {
					t.Fatalf("couldn't get initial state: %v", err)
				}
			}

			err := z.SetProperty(tc.propertyName, tc.propertyValue, tc.dataset, tc.force)

			if err != nil {
				if !tc.wantErr {
					t.Fatalf("expected no error but got: %v", err)
				}
				// we don't return because we want to check that on error, SetProperty() is a no-op
			}
			if err == nil && tc.wantErr {
				t.Fatal("expected an error but got none")
			}

			got, err := z.Scan()
			if err != nil {
				t.Fatalf("couldn't get final state: %v", err)
			}

			if tc.isNoOp {
				assertDatasetsEquals(t, ta, initState, got, true)
				return
			}
			assertDatasetsToGolden(t, ta, got, true)
		})
	}
}

func TestTransactions(t *testing.T) {
	skipOnZFSPermissionDenied(t)

	tests := map[string]struct {
		def           string
		doSnapshot    bool
		doClone       bool
		doPromote     bool
		doSetProperty bool
		doDestroy     bool
		shouldErr     bool
		revert        bool
	}{
		"Snapshot only, success, Done":   {def: "layout1_for_transactions_tests.yaml", doSnapshot: true},
		"Snapshot only, success, Revert": {def: "layout1_for_transactions_tests.yaml", doSnapshot: true, revert: true},
		"Snapshot only, fail, Revert":    {def: "layout1_for_transactions_tests.yaml", doSnapshot: true, shouldErr: true, revert: true},
		"Snapshot only, fail, No revert": {def: "layout1_for_transactions_tests.yaml", doSnapshot: true, shouldErr: true}, // will issue a warning

		"Clone only, success, Done":   {def: "layout1_for_transactions_tests.yaml", doClone: true, revert: true},
		"Clone only, success, Revert": {def: "layout1_for_transactions_tests.yaml", doClone: true},
		// We unfortunately can't do those because we can't fail in the middle of Clone(), after some modification were done
		// The 2 failures are: either the dataset exists with suffix (won't clone anything) or missing intermediate snapshot
		// (won't even start cloning).
		// Avoid special casing the test code for no benefits.
		//"Clone only, fail, Revert":    {def: "layout1_for_transactions_tests.yaml", doClone: true, shouldErr: true, revert: true},
		//"Clone only, fail, No revert": {def: "layout1_for_transactions_tests.yaml", doClone: true, shouldErr: true}, // will issue a warning

		"Promote only, success, Done":   {def: "layout1_for_transactions_tests.yaml", doPromote: true, revert: true},
		"Promote only, success, Revert": {def: "layout1_for_transactions_tests.yaml", doPromote: true},
		// We unfortunately can't do those because we can't fail in the middle of Promote(), after some modification were done

		"SetProperty only, success, Done":   {def: "layout1_for_transactions_tests.yaml", doSetProperty: true, revert: true},
		"SetProperty only, success, Revert": {def: "layout1_for_transactions_tests.yaml", doSetProperty: true},
		// We unfortunately can't do those because we can't fail in the middle of SetProperty(), after some modification were done

		// Destroy can't be in transactions
		"Destroy, failed before doing anything": {def: "layout1_for_transactions_tests.yaml", doDestroy: true},

		"Multiple steps transaction, success, Done":   {def: "layout1_for_transactions_tests.yaml", doSnapshot: true, doClone: true, doPromote: true, doSetProperty: true},
		"Multiple steps transaction, success, Revert": {def: "layout1_for_transactions_tests.yaml", doSnapshot: true, doClone: true, doPromote: true, doSetProperty: true, revert: true},
		"Multiple steps transaction, fail, Revert":    {def: "layout1_for_transactions_tests.yaml", doSnapshot: true, doClone: true, doPromote: true, doSetProperty: true, shouldErr: true, revert: true},
		"Multiple steps transaction, fail, No revert": {def: "layout1_for_transactions_tests.yaml", doSnapshot: true, doClone: true, doPromote: true, doSetProperty: true, shouldErr: true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			dir, cleanup := tempDir(t)
			defer cleanup()

			ta := timeAsserter(time.Now())
			fPools := newFakePools(t, filepath.Join("testdata", tc.def))
			defer fPools.create(dir)()
			z := zfs.New(zfs.WithTransactions())
			initState, err := z.Scan()
			if err != nil {
				t.Fatalf("couldn't get initial state: %v", err)
			}
			state := initState

			if tc.doSnapshot {
				snapName := "snap1"
				datasetName := "rpool/ROOT/ubuntu_1234"
				if tc.shouldErr {
					// an existing snapshot on var/lib exists and will make it fail
					snapName = "snap_r1"
					datasetName = "rpool/ROOT/ubuntu_1234/var"
				}
				err := z.Snapshot(snapName, datasetName, true)
				if !tc.shouldErr && err != nil {
					t.Fatalf("taking snapshot shouldn't have failed but it did: %v", err)
				} else if tc.shouldErr && err == nil {
					t.Fatal("taking snapshot should have returned an error but it didn't")
				}
				// We expect some modifications
				newState, err := z.Scan()
				if err != nil {
					t.Fatalf("couldn't get state after snapshot: %v", err)
				}
				assertDatasetsNotEquals(t, ta, state, newState, true)
				state = newState
			}

			if tc.doClone {
				name := "rpool/ROOT/ubuntu_1234@snap_r2"
				suffix := "5678"
				if tc.shouldErr {
					// rpool/ROOT/ubuntu_9999 exists
					suffix = "9999"
				}
				err := z.Clone(name, suffix, false, true)
				if !tc.shouldErr && err != nil {
					t.Fatalf("cloning shouldn't have failed but it did: %v", err)
				} else if tc.shouldErr && err == nil {
					t.Fatal("cloning should have returned an error but it didn't")
				}
				// We can't expect some modifications (see above)
				newState, err := z.Scan()
				if err != nil {
					t.Fatalf("couldn't get state after cloning: %v", err)
				}
				// bypass checks on error (things will be equal), as we can't have an error with changes see the test definition above
				if !tc.shouldErr {
					assertDatasetsNotEquals(t, ta, state, newState, true)
				}
				state = newState
			}

			if tc.doPromote {
				name := "rpool/ROOT/ubuntu_5678"
				if tc.shouldErr {
					// rpool/ROOT/ubuntu_1111 doesn't exists
					name = "rpool/ROOT/ubuntu_1111"
				} else {
					// Prepare cloning in its own transaction
					if !tc.doClone {
						z2 := zfs.New()
						err := z2.Clone("rpool/ROOT/ubuntu_1234@snap_r2", "5678", false, true)
						if err != nil {
							t.Fatalf("couldnt clone to prepare dataset hierarchy: %v", err)
						}
						// Reset init state
						initState, err = z.Scan()
						if err != nil {
							t.Fatalf("couldn't get initial state: %v", err)
						}
						state = initState
					}
				}
				err := z.Promote(name)
				if !tc.shouldErr && err != nil {
					t.Fatalf("promoting shouldn't have failed but it did: %v", err)
				} else if tc.shouldErr && err == nil {
					t.Fatal("promoting should have returned an error but it didn't")
				}
				// We can't expect some modifications (see above)
				newState, err := z.Scan()
				if err != nil {
					t.Fatalf("couldn't get state after promoting: %v", err)
				}
				// bypass checks on error (things will be equal), as we can't have an error with changes see the test definition above
				if !tc.shouldErr {
					assertDatasetsNotEquals(t, ta, state, newState, true)
				}
				state = newState
			}

			if tc.doDestroy {
				if err := z.Destroy("rpool/ROOT/ubuntu_1234"); err == nil {
					t.Fatalf("expected destroy to not work in transactions, but it returned no error")
				}
				// Expect no modifications
				newState, err := z.Scan()
				if err != nil {
					t.Fatalf("couldn't get state after destruction: %v", err)
				}
				assertDatasetsEquals(t, ta, state, newState, true)
				return
			}

			if tc.doSetProperty {
				propertyName := zfs.BootfsProp
				if tc.shouldErr {
					// this property isn't allowed
					propertyName = "mountpoint"
				}
				err := z.SetProperty(propertyName, "no", "rpool/ROOT/ubuntu_1234", false)
				if !tc.shouldErr && err != nil {
					t.Fatalf("changing property shouldn't have failed but it did: %v", err)
				} else if tc.shouldErr && err == nil {
					t.Fatal("changing property should have returned an error but it didn't")
				}
				// We expect some modifications
				newState, err := z.Scan()
				if err != nil {
					t.Fatalf("couldn't get state after snapshot: %v", err)
				}
				// bypass checks on error (things will be equal), as we can't have an error with changes see the test definition above
				if !tc.shouldErr {
					assertDatasetsNotEquals(t, ta, state, newState, true)
				}
				state = newState
			}

			// Final transaction states
			if tc.revert {
				// Revert: should get back to initial state
				z.Cancel()
				newState, err := z.Scan()
				if err != nil {
					t.Fatalf("couldn't get finale state: %v", err)
				}
				assertDatasetsNotEquals(t, ta, state, newState, true)
				assertDatasetsEquals(t, ta, initState, newState, true)
			} else {
				// Done: should commit the current state and be different from initial one
				z.Done()
				newState, err := z.Scan()
				if err != nil {
					t.Fatalf("couldn't get finale state: %v", err)
				}
				assertDatasetsEquals(t, ta, state, newState, true)
				assertDatasetsNotEquals(t, ta, initState, newState, true)
			}
		})
	}
}

// transformToReproducibleDatasetSlice applied transformation to ensure that the comparison is reproducible via
// DataSlices.
func transformToReproducibleDatasetSlice(t *testing.T, ta timeAsserter, got []zfs.Dataset, includePrivate bool) zfs.DatasetSlice {
	t.Helper()

	// Ensure datasets were created at expected range time and replace them with magic time.
	var ds []*zfs.Dataset
	for k := range got {
		if !got[k].IsSnapshot {
			continue
		}
		ds = append(ds, &got[k])
	}
	ta.assertAndReplaceCreationTimeInRange(t, ds)

	// Sort the golden file order to be reproducible.
	gotForGolden := zfs.DatasetSlice{DS: got, IncludePrivate: includePrivate}
	sort.Sort(gotForGolden)
	return gotForGolden
}

// datasetsEquals prints a diff if datasets aren't equals and fails the test
func datasetsEquals(t *testing.T, want, got []zfs.Dataset, includePrivate bool) {
	t.Helper()

	// Actual diff assertion.
	privateOpt := cmpopts.IgnoreUnexported(zfs.DatasetProp{})
	if includePrivate {
		privateOpt = cmp.AllowUnexported(zfs.DatasetProp{})
	}
	if diff := cmp.Diff(want, got, privateOpt); diff != "" {
		t.Errorf("Scan() mismatch (-want +got):\n%s", diff)
	}
}

// datasetsNotEquals prints the struct if datasets are equals and fails the test
func datasetsNotEquals(t *testing.T, want, got []zfs.Dataset, includePrivate bool) {
	t.Helper()

	// Actual diff assertion.
	privateOpt := cmpopts.IgnoreUnexported(zfs.DatasetProp{})
	if includePrivate {
		privateOpt = cmp.AllowUnexported(zfs.DatasetProp{})
	}
	if diff := cmp.Diff(want, got, privateOpt); diff == "" {
		t.Error("datasets are equals where we expected not to:", pp.Sprint(want))
	}
}

// assertDatasetsToGolden compares (and update if needed) a slice of dataset got from a Scan() for instance
// to a golden file.
// We can optionnally include private fields in the comparison and saving.
func assertDatasetsToGolden(t *testing.T, ta timeAsserter, got []zfs.Dataset, includePrivate bool) {
	t.Helper()

	gotForGolden := transformToReproducibleDatasetSlice(t, ta, got, includePrivate)
	got = gotForGolden.DS

	// Get expected dataset list from golden file, update as needed.
	wantFromGolden := zfs.DatasetSlice{IncludePrivate: includePrivate}
	testutils.LoadFromGoldenFile(t, gotForGolden, &wantFromGolden)
	want := []zfs.Dataset(wantFromGolden.DS)

	datasetsEquals(t, want, got, includePrivate)
}

// assertDatasetsEquals compares 2 slices of datasets, after ensuring they can be reproducible.
// We can optionnally include private fields in the comparison.
func assertDatasetsEquals(t *testing.T, ta timeAsserter, want, got []zfs.Dataset, includePrivate bool) {
	t.Helper()

	want = transformToReproducibleDatasetSlice(t, ta, want, includePrivate).DS
	got = transformToReproducibleDatasetSlice(t, ta, got, includePrivate).DS

	datasetsEquals(t, want, got, includePrivate)
}

// assertDatasetsNotEquals compares 2 slices of datasets, ater ensuring they can be reproducible.
// We can optionnally include private fields in the comparison.
func assertDatasetsNotEquals(t *testing.T, ta timeAsserter, want, got []zfs.Dataset, includePrivate bool) {
	t.Helper()

	want = transformToReproducibleDatasetSlice(t, ta, want, includePrivate).DS
	got = transformToReproducibleDatasetSlice(t, ta, got, includePrivate).DS

	datasetsNotEquals(t, want, got, includePrivate)
}

func tempDir(t *testing.T) (string, func()) {
	t.Helper()

	dir, err := ioutil.TempDir("", "zsystest-")
	if err != nil {
		t.Fatal("can't create temporary directory", err)
	}
	return dir, func() {
		if err = os.RemoveAll(dir); err != nil {
			t.Error("can't clean temporary directory", err)
		}
	}
}

// timeAsserter ensures that dates will be between a start and end time
type timeAsserter time.Time

const currentMagicTime = 2000000000

// assertAndReplaceCreationTimeInRange ensure that last-used is between starts and endtime.
// It replaces those datasets last-used with the current fake "current time"
func (ta timeAsserter) assertAndReplaceCreationTimeInRange(t *testing.T, ds []*zfs.Dataset) {
	t.Helper()
	curr := time.Now().Unix()
	start := time.Time(ta).Unix()

	for _, r := range ds {
		// avoid transforming already set MagicTime
		if r.LastUsed == currentMagicTime {
			continue
		}

		if int64(r.LastUsed) < start || int64(r.LastUsed) > curr {
			t.Errorf("expected snapshot time outside of range: %d", r.LastUsed)
		} else {
			r.LastUsed = currentMagicTime
		}
	}
}

// skipOnZFSPermissionDenied skips the tests if the current user can't create zfs pools, datasets…
func skipOnZFSPermissionDenied(t *testing.T) {
	t.Helper()

	u, err := user.Current()
	if err != nil {
		t.Fatal("can't get current user", err)
	}

	// in our default setup, only root users can interact with zfs kernel modules
	if u.Uid != "0" {
		t.Skip("skipping, you don't have permissions to interact with system zfs")
	}
}
