package gitcmd

import (
	"context"
	"reflect"
	"testing"
)

// Cherry tells squash-merged commits (same patch, different SHA) from those
// still missing upstream, and reports nothing when there is nothing to compare.
func TestCherry_SplitsAppliedFromPending(t *testing.T) {
	dir := initRepo(t)
	runGit(t, dir, "checkout", "-q", "-b", "feature")
	a := commit(t, dir, "add a", map[string]string{"a.txt": "a"})
	b := commit(t, dir, "add b", map[string]string{"b.txt": "b"})
	runGit(t, dir, "checkout", "-q", "main")
	commit(t, dir, "squash: add a", map[string]string{"a.txt": "a"})
	r := newRunner(t, dir)

	applied, pending, err := r.Cherry(context.Background(), "main", "feature")
	if err != nil || !reflect.DeepEqual(applied, []string{a}) || !reflect.DeepEqual(pending, []string{b}) {
		t.Errorf("Cherry(main, feature) = %v, %v, %v; want [%s], [%s]", applied, pending, err, a, b)
	}
	applied, pending, err = r.Cherry(context.Background(), "main", "main")
	if err != nil || applied != nil || pending != nil {
		t.Errorf("Cherry(main, main) = %v, %v, %v; want nil, nil", applied, pending, err)
	}
}

func TestCherry_RejectsEmptyRefs(t *testing.T) {
	r := newRunner(t, initRepo(t))
	for _, c := range [][2]string{{"", "HEAD"}, {"HEAD", ""}} {
		_, _, err := r.Cherry(context.Background(), c[0], c[1])
		asGitError(t, err)
	}
}
