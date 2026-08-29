package lspclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/zhenxin-dev/banka-code/internal/tools"
)

// ApplyWorkspaceEdit applies a protocol WorkspaceEdit within workspaceRoot.
// It validates every target and edit before writing any file.
func ApplyWorkspaceEdit(ctx context.Context, workspaceRoot string, edit WorkspaceEdit) error {
	return applyWorkspaceEdit(ctx, workspaceRoot, edit, nil)
}

func applyWorkspaceEdit(ctx context.Context, workspaceRoot string, edit workspaceEdit, resolver func(string) (string, error)) error {
	if ctx == nil {
		ctx = context.Background()
	}
	root, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return err
	}
	operations, err := planWorkspaceOperations(edit)
	if err != nil {
		return err
	}
	// Resolve and validate every URI before the first mutation. This keeps a
	// malformed or out-of-workspace server response from partially changing the
	// workspace. Text ranges are validated against a virtual filesystem so an
	// ordered create/rename followed by an edit is handled correctly.
	if err := preflightWorkspaceOperations(root, operations, resolver); err != nil {
		return err
	}
	snapshot, err := snapshotWorkspaceEdit(root, edit, resolver)
	if err != nil {
		return err
	}
	for _, operation := range operations {
		if err := ctx.Err(); err != nil {
			_ = restoreWorkspaceSnapshot(snapshot)
			return err
		}
		if operation.kind == "text" {
			path, err := resolveEditPath(root, operation.uri, resolver)
			if err != nil {
				_ = restoreWorkspaceSnapshot(snapshot)
				return err
			}
			content, err := os.ReadFile(path)
			if err != nil {
				_ = restoreWorkspaceSnapshot(snapshot)
				return fmt.Errorf("read workspace edit target %s: %w", operation.uri, err)
			}
			updated, err := applyTextEdits(content, operation.edits)
			if err != nil {
				_ = restoreWorkspaceSnapshot(snapshot)
				return fmt.Errorf("apply workspace edits for %s: %w", operation.uri, err)
			}
			if err := atomicWrite(path, updated); err != nil {
				_ = restoreWorkspaceSnapshot(snapshot)
				return err
			}
			continue
		}
		if err := applyResourceOperation(root, operation.resource, resolver); err != nil {
			_ = restoreWorkspaceSnapshot(snapshot)
			return err
		}
	}
	return nil
}

type virtualFileState struct {
	exists  bool
	isDir   bool
	content []byte
	loaded  bool
}

func preflightWorkspaceOperations(root string, operations []workspaceOperation, resolver func(string) (string, error)) error {
	states := make(map[string]virtualFileState)
	load := func(path string) (virtualFileState, error) {
		if state, exists := states[path]; exists {
			return state, nil
		}
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			state := virtualFileState{}
			states[path] = state
			return state, nil
		}
		if err != nil {
			return virtualFileState{}, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return virtualFileState{}, fmt.Errorf("workspace edit target is a symbolic link: %s", path)
		}
		state := virtualFileState{exists: true, isDir: info.IsDir()}
		if !state.isDir {
			content, readErr := os.ReadFile(path)
			if readErr != nil {
				return virtualFileState{}, readErr
			}
			state.content = content
			state.loaded = true
		}
		states[path] = state
		return state, nil
	}
	loadTree := func(path string) error {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("workspace edit target is a symbolic link: %s", path)
		}
		if !info.IsDir() {
			_, err := load(path)
			return err
		}
		count := 0
		return filepath.WalkDir(path, func(current string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			count++
			if count > maxSnapshotEntries {
				return errors.New("workspace edit preflight is too large")
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return errors.New("workspace edit target contains a symbolic link")
			}
			_, err := load(current)
			return err
		})
	}
	removeVirtual := func(path string) {
		for candidate := range states {
			if candidate == path || strings.HasPrefix(candidate, path+string(filepath.Separator)) {
				delete(states, candidate)
			}
		}
		states[path] = virtualFileState{}
	}
	moveVirtual := func(oldPath string, newPath string) {
		moved := make(map[string]virtualFileState)
		prefix := oldPath + string(filepath.Separator)
		for candidate, state := range states {
			if candidate == oldPath {
				moved[newPath] = state
				delete(states, candidate)
				continue
			}
			if strings.HasPrefix(candidate, prefix) {
				relative := strings.TrimPrefix(candidate, prefix)
				moved[filepath.Join(newPath, relative)] = state
				delete(states, candidate)
			}
		}
		for candidate, state := range moved {
			states[candidate] = state
		}
		// Keep a tombstone for the old root.  Otherwise a later operation that
		// still refers to the pre-rename URI would accidentally re-read the
		// unchanged on-disk source during preflight.
		states[oldPath] = virtualFileState{}
	}
	for _, operation := range operations {
		switch operation.kind {
		case "text":
			path, err := resolveEditPath(root, operation.uri, resolver)
			if err != nil {
				return err
			}
			state, err := load(path)
			if err != nil {
				return fmt.Errorf("inspect workspace edit target %s: %w", operation.uri, err)
			}
			if !state.exists {
				return fmt.Errorf("workspace edit target does not exist: %s", operation.uri)
			}
			if state.isDir {
				return fmt.Errorf("workspace edit target is a directory: %s", operation.uri)
			}
			updated, err := applyTextEdits(state.content, operation.edits)
			if err != nil {
				return fmt.Errorf("validate workspace edits for %s: %w", operation.uri, err)
			}
			state.content = updated
			state.loaded = true
			states[path] = state
		case "create":
			path, err := resolveEditPath(root, operation.resource.URI, resolver)
			if err != nil {
				return err
			}
			options, err := decodeResourceOptions(operation.resource)
			if err != nil {
				return err
			}
			state, err := load(path)
			if err != nil {
				return err
			}
			if state.exists && !options.Overwrite && !options.IgnoreIfExists {
				return fmt.Errorf("workspace create target already exists: %s", path)
			}
			if state.exists && options.IgnoreIfExists && !options.Overwrite {
				continue
			}
			if state.exists && state.isDir && options.Overwrite {
				return fmt.Errorf("workspace create target is a directory: %s", path)
			}
			if !state.exists || options.Overwrite {
				states[path] = virtualFileState{exists: true, content: nil, loaded: true}
			}
		case "rename":
			oldPath, err := resolveEditPath(root, operation.resource.OldURI, resolver)
			if err != nil {
				return err
			}
			newPath, err := resolveEditPath(root, operation.resource.NewURI, resolver)
			if err != nil {
				return err
			}
			if oldPath == newPath {
				return errors.New("workspace rename source and destination are identical")
			}
			if strings.HasPrefix(newPath, oldPath+string(filepath.Separator)) {
				return errors.New("workspace rename destination cannot be inside its source directory")
			}
			options, err := decodeResourceOptions(operation.resource)
			if err != nil {
				return err
			}
			oldState, err := load(oldPath)
			if err != nil {
				return err
			}
			if !oldState.exists {
				return fmt.Errorf("workspace rename source does not exist: %s", oldPath)
			}
			newState, err := load(newPath)
			if err != nil {
				return err
			}
			if newState.exists && !options.Overwrite && !options.IgnoreIfExists {
				return fmt.Errorf("workspace rename target already exists: %s", newPath)
			}
			if newState.exists && options.IgnoreIfExists && !options.Overwrite {
				continue
			}
			if oldState.isDir {
				if err := loadTree(oldPath); err != nil {
					return err
				}
			}
			if options.Overwrite {
				removeVirtual(newPath)
			}
			moveVirtual(oldPath, newPath)
			// A file/directory that was loaded as the source always moves, even
			// when it has no descendants.  Keep the explicit root state for the
			// common lazy-load case.
			if _, exists := states[newPath]; !exists {
				states[newPath] = oldState
			}
		case "delete":
			path, err := resolveEditPath(root, operation.resource.URI, resolver)
			if err != nil {
				return err
			}
			options, err := decodeResourceOptions(operation.resource)
			if err != nil {
				return err
			}
			state, err := load(path)
			if err != nil {
				return err
			}
			if !state.exists {
				if options.IgnoreIfNotExists {
					continue
				}
				return fmt.Errorf("workspace delete target does not exist: %s", path)
			}
			if state.isDir && !options.Recursive {
				for candidate, child := range states {
					if candidate != path && strings.HasPrefix(candidate, path+string(filepath.Separator)) && child.exists {
						return fmt.Errorf("workspace delete target is a non-empty directory: %s", path)
					}
				}
				if _, readErr := os.ReadDir(path); readErr == nil {
					entries, _ := os.ReadDir(path)
					if len(entries) > 0 {
						return fmt.Errorf("workspace delete target is a non-empty directory: %s", path)
					}
				}
			}
			removeVirtual(path)
		default:
			return fmt.Errorf("unsupported workspace resource operation: %s", operation.kind)
		}
	}
	return nil
}

func decodeResourceOptions(operation resourceOperation) (resourceOperationOptions, error) {
	options := resourceOperationOptions{}
	if len(operation.Options) > 0 {
		if err := json.Unmarshal(operation.Options, &options); err != nil {
			return options, fmt.Errorf("decode %s operation options: %w", operation.Kind, err)
		}
	}
	return options, nil
}

type workspaceOperation struct {
	kind     string
	uri      string
	edits    []textEdit
	resource resourceOperation
}

func planWorkspaceOperations(edit workspaceEdit) ([]workspaceOperation, error) {
	operations := make([]workspaceOperation, 0, len(edit.Changes)+len(edit.DocumentChanges))
	// The legacy changes map has no ordering contract. Sort its URIs so that
	// repeated runs are deterministic, then append documentChanges in protocol
	// order (the latter is important around file renames and creates).
	uris := make([]string, 0, len(edit.Changes))
	for uri := range edit.Changes {
		uris = append(uris, uri)
	}
	sort.Strings(uris)
	for _, uri := range uris {
		if len(edit.Changes[uri]) > 0 {
			operations = append(operations, workspaceOperation{kind: "text", uri: uri, edits: append([]textEdit(nil), edit.Changes[uri]...)})
		}
	}
	// TextDocumentEdit ranges are all expressed against the same document
	// version.  Coalesce adjacent edits per URI and flush them before a resource
	// operation that touches that URI (or one of its descendants), matching the
	// ordering rules in LSP §3.16.2.  Applying each TextDocumentEdit separately
	// would shift later ranges and can silently corrupt imports/references.
	pending := make(map[string][]textEdit)
	flushURI := func(uri string) {
		edits, ok := pending[uri]
		if !ok {
			return
		}
		delete(pending, uri)
		if len(edits) > 0 {
			operations = append(operations, workspaceOperation{kind: "text", uri: uri, edits: edits})
		}
	}
	flushAll := func() {
		uris := make([]string, 0, len(pending))
		for uri := range pending {
			uris = append(uris, uri)
		}
		sort.Strings(uris)
		for _, uri := range uris {
			flushURI(uri)
		}
	}
	for _, raw := range edit.DocumentChanges {
		var withText struct {
			TextDocument *struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
			Edits []textEdit `json:"edits"`
		}
		if json.Unmarshal(raw, &withText) == nil && withText.TextDocument != nil && withText.TextDocument.URI != "" {
			if len(withText.Edits) > 0 {
				uri := withText.TextDocument.URI
				pending[uri] = append(pending[uri], withText.Edits...)
			}
			continue
		}
		var resource resourceOperation
		if err := json.Unmarshal(raw, &resource); err != nil {
			return nil, fmt.Errorf("decode workspace edit operation: %w", err)
		}
		if resource.Kind == "" {
			return nil, errors.New("workspace edit contains an unsupported document change")
		}
		// Resource operations are ordered relative to every preceding
		// TextDocumentEdit, not only edits touching the same URI. Flush the
		// pending batch before appending a resource operation so a create/rename/
		// delete cannot leapfrog an earlier text edit on another file.
		flushAll()
		switch resource.Kind {
		case "create":
			// flushAll above already preserves the protocol order.
		case "rename":
			// flushAll above already preserves the protocol order.
		case "delete":
			// flushAll above already preserves the protocol order.
		default:
			return nil, fmt.Errorf("unsupported workspace resource operation: %s", resource.Kind)
		}
		operations = append(operations, workspaceOperation{kind: resource.Kind, resource: resource})
	}
	// Flush text edits that were not followed by a resource operation in stable
	// URI order.  This also makes malformed/repeated server output deterministic.
	remaining := make([]string, 0, len(pending))
	for uri := range pending {
		remaining = append(remaining, uri)
	}
	sort.Strings(remaining)
	for _, uri := range remaining {
		flushURI(uri)
	}
	return operations, nil
}

func resolveEditPath(root string, uri string, resolver func(string) (string, error)) (string, error) {
	path, err := uriToPath(uri)
	if err != nil {
		return "", err
	}
	if resolver != nil {
		resolved, err := resolver(path)
		if err != nil {
			return "", err
		}
		// A resolver may add policy (for example permission-aware path
		// resolution), but it must not weaken the LSP workspace boundary.  Always
		// validate its result as well; otherwise a buggy/custom resolver could
		// make an otherwise safe server response write outside the workspace.
		resolved, err = safeWorkspacePath(root, resolved)
		if err != nil {
			return "", err
		}
		if filepath.Clean(resolved) == filepath.Clean(root) {
			return "", errors.New("workspace edits may not target the workspace root")
		}
		return resolved, nil
	}
	resolved, err := safeWorkspacePath(root, path)
	if err != nil {
		return "", err
	}
	if filepath.Clean(resolved) == filepath.Clean(root) {
		return "", errors.New("workspace edits may not target the workspace root")
	}
	return resolved, nil
}

func constrainedEditResolver(root string) func(string) (string, error) {
	return func(target string) (string, error) {
		return safeWorkspacePath(root, target)
	}
}

func validateWorkspaceOperation(root string, operation workspaceOperation, resolver func(string) (string, error)) error {
	switch operation.kind {
	case "text":
		path, err := resolveEditPath(root, operation.uri, resolver)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			// A create operation earlier in the same ordered edit may make this
			// path available only during execution; defer range validation then.
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("read workspace edit target %s: %w", operation.uri, err)
		}
		if err := validateTextEdits(content, operation.edits); err != nil {
			return fmt.Errorf("validate workspace edits for %s: %w", operation.uri, err)
		}
	case "create", "rename", "delete":
		return validateResourceOperation(root, operation.resource, resolver)
	default:
		return fmt.Errorf("unsupported workspace resource operation: %s", operation.kind)
	}
	return nil
}

func validateResourceOperation(root string, operation resourceOperation, resolver func(string) (string, error)) error {
	uris := []string{}
	switch operation.Kind {
	case "create", "delete":
		uris = append(uris, operation.URI)
	case "rename":
		uris = append(uris, operation.OldURI, operation.NewURI)
	default:
		return fmt.Errorf("unsupported workspace resource operation: %s", operation.Kind)
	}
	for _, uri := range uris {
		if strings.TrimSpace(uri) == "" {
			return fmt.Errorf("workspace %s operation contains an empty URI", operation.Kind)
		}
		if _, err := resolveEditPath(root, uri, resolver); err != nil {
			return err
		}
	}
	return nil
}

type resourceOperation struct {
	Kind    string          `json:"kind"`
	OldURI  string          `json:"oldUri"`
	NewURI  string          `json:"newUri"`
	URI     string          `json:"uri"`
	Options json.RawMessage `json:"options,omitempty"`
}

type resourceOperationOptions struct {
	Overwrite         bool `json:"overwrite"`
	IgnoreIfExists    bool `json:"ignoreIfExists"`
	IgnoreIfNotExists bool `json:"ignoreIfNotExists"`
	Recursive         bool `json:"recursive"`
}

const maxRenameFiles = 2000

type renamePathPair struct {
	oldPath string
	newPath string
}

// enumerateRenamePairs returns the file-level URI pairs expected by
// workspace/willRenameFiles. Directory renames are expanded deterministically
// and bounded so a giant tree cannot monopolize a request.
func enumerateRenamePairs(source string, destination string, sourceInfo os.FileInfo) ([]renamePathPair, error) {
	if !sourceInfo.IsDir() {
		return []renamePathPair{{oldPath: source, newPath: destination}}, nil
	}
	pairs := make([]renamePathPair, 0)
	err := filepath.WalkDir(source, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == source {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(source, current)
		if err != nil {
			return err
		}
		pairs = append(pairs, renamePathPair{oldPath: current, newPath: filepath.Join(destination, relative)})
		if len(pairs) > maxRenameFiles {
			return errors.New("LSP rename_file directory contains too many regular files")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].oldPath < pairs[j].oldPath })
	return pairs, nil
}

func mergeWorkspaceEdits(target *workspaceEdit, source workspaceEdit) {
	if target.Changes == nil {
		target.Changes = make(map[string][]textEdit)
	}
	for uri, edits := range source.Changes {
		if len(edits) == 0 {
			continue
		}
		for _, edit := range edits {
			duplicate := false
			for _, existing := range target.Changes[uri] {
				if existing.Range == edit.Range && existing.NewText == edit.NewText {
					duplicate = true
					break
				}
			}
			if !duplicate {
				target.Changes[uri] = append(target.Changes[uri], edit)
			}
		}
	}
	target.DocumentChanges = append(target.DocumentChanges, source.DocumentChanges...)
	if len(target.Changes) == 0 {
		target.Changes = nil
	}
}

// applyWorkspaceEditAndRename applies semantic edits and then moves a file or
// directory. A small rollback journal protects the common failure case where
// the filesystem move fails after text edits have been written.
func applyWorkspaceEditAndRename(ctx context.Context, root string, edit workspaceEdit, resolver func(string) (string, error), source string, destination string) error {
	snapshot, err := snapshotWorkspaceEdit(root, edit, resolver)
	if err != nil {
		return err
	}
	// The final move is outside the WorkspaceEdit payload.  Include both paths
	// in the journal as well, so a failed mkdir/rename (or a server edit that
	// moves one of them first) restores the complete rename transaction.
	seen := make(map[string]bool, len(snapshot))
	for _, item := range snapshot {
		seen[item.path] = true
	}
	entries, bytesTaken := 0, 0
	extraPaths := []string{source, destination}
	for _, target := range []string{source, destination} {
		for parent := filepath.Dir(target); parent != root && parent != filepath.Dir(parent); parent = filepath.Dir(parent) {
			if _, statErr := os.Lstat(parent); !errors.Is(statErr, os.ErrNotExist) {
				break
			}
			extraPaths = append(extraPaths, parent)
		}
	}
	// Snapshot ancestors before their children so reverse restoration removes
	// newly-created descendants before an originally missing parent directory.
	sort.SliceStable(extraPaths, func(i, j int) bool {
		if len(extraPaths[i]) != len(extraPaths[j]) {
			return len(extraPaths[i]) < len(extraPaths[j])
		}
		return extraPaths[i] < extraPaths[j]
	})
	for _, path := range extraPaths {
		if seen[path] {
			continue
		}
		item, snapshotErr := snapshotPath(path, &entries, &bytesTaken)
		if snapshotErr != nil {
			return snapshotErr
		}
		snapshot = append(snapshot, item)
		seen[path] = true
	}
	if err := applyWorkspaceEdit(ctx, root, edit, resolver); err != nil {
		_ = restoreWorkspaceSnapshot(snapshot)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		_ = restoreWorkspaceSnapshot(snapshot)
		return err
	}
	if err := os.Rename(source, destination); err != nil {
		_ = restoreWorkspaceSnapshot(snapshot)
		return err
	}
	return nil
}

type workspaceFileSnapshot struct {
	path      string
	exists    bool
	mode      os.FileMode
	content   []byte
	directory bool
	children  []workspaceFileSnapshot
}

const (
	maxSnapshotEntries = 20_000
	maxSnapshotBytes   = 128 * 1024 * 1024
)

func snapshotWorkspaceEdit(root string, edit workspaceEdit, resolver func(string) (string, error)) ([]workspaceFileSnapshot, error) {
	operations, err := planWorkspaceOperations(edit)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(operations))
	seen := make(map[string]bool)
	for _, operation := range operations {
		uris := []string{}
		if operation.kind == "text" {
			uris = append(uris, operation.uri)
		} else {
			switch operation.kind {
			case "create", "delete":
				uris = append(uris, operation.resource.URI)
			case "rename":
				uris = append(uris, operation.resource.OldURI, operation.resource.NewURI)
			}
		}
		for _, uri := range uris {
			path, resolveErr := resolveEditPath(root, uri, resolver)
			if resolveErr != nil {
				return nil, resolveErr
			}
			if !seen[path] {
				seen[path] = true
				paths = append(paths, path)
			}
			// Resource operations may create parent directories during execution.
			// Remember missing ancestors as well so a failed transaction does not
			// leave an otherwise empty directory tree behind.
			for parent := filepath.Dir(path); parent != root && parent != filepath.Dir(parent); parent = filepath.Dir(parent) {
				if _, statErr := os.Lstat(parent); !errors.Is(statErr, os.ErrNotExist) {
					break
				}
				if !seen[parent] {
					seen[parent] = true
					paths = append(paths, parent)
				}
			}
		}
	}
	// Prefer an ancestor snapshot over nested snapshots.  A directory snapshot
	// already contains every descendant and restoring both independently can
	// reintroduce files in the wrong order.
	sort.Slice(paths, func(i, j int) bool {
		if len(paths[i]) != len(paths[j]) {
			return len(paths[i]) < len(paths[j])
		}
		return paths[i] < paths[j]
	})
	filtered := paths[:0]
	for _, path := range paths {
		redundant := false
		for _, parent := range filtered {
			relative, relErr := filepath.Rel(parent, path)
			if relErr == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
				redundant = true
				break
			}
		}
		if !redundant {
			filtered = append(filtered, path)
		}
	}
	paths = filtered
	snapshots := make([]workspaceFileSnapshot, 0, len(paths))
	entries, bytesTaken := 0, 0
	for _, path := range paths {
		snapshot, snapshotErr := snapshotPath(path, &entries, &bytesTaken)
		if snapshotErr != nil {
			return nil, snapshotErr
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}

func snapshotPath(path string, entries *int, bytesTaken *int) (workspaceFileSnapshot, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return workspaceFileSnapshot{path: path}, nil
	}
	if err != nil {
		return workspaceFileSnapshot{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return workspaceFileSnapshot{}, fmt.Errorf("workspace edit target is a symbolic link: %s", path)
	}
	if info.IsDir() {
		if *entries >= maxSnapshotEntries {
			return workspaceFileSnapshot{}, errors.New("workspace edit snapshot is too large")
		}
		*entries = *entries + 1
		directory := workspaceFileSnapshot{path: path, exists: true, mode: info.Mode(), directory: true}
		children, readErr := os.ReadDir(path)
		if readErr != nil {
			return workspaceFileSnapshot{}, readErr
		}
		sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
		for _, child := range children {
			childSnapshot, childErr := snapshotPath(filepath.Join(path, child.Name()), entries, bytesTaken)
			if childErr != nil {
				return workspaceFileSnapshot{}, childErr
			}
			directory.children = append(directory.children, childSnapshot)
		}
		return directory, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return workspaceFileSnapshot{}, err
	}
	if *entries >= maxSnapshotEntries || *bytesTaken > maxSnapshotBytes-len(content) {
		return workspaceFileSnapshot{}, errors.New("workspace edit snapshot is too large")
	}
	*entries = *entries + 1
	*bytesTaken += len(content)
	return workspaceFileSnapshot{path: path, exists: true, mode: info.Mode(), content: content}, nil
}

func restoreWorkspaceSnapshot(snapshots []workspaceFileSnapshot) error {
	var firstErr error
	for index := len(snapshots) - 1; index >= 0; index-- {
		if err := restoreSnapshotNode(snapshots[index]); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func restoreSnapshotNode(snapshot workspaceFileSnapshot) error {
	info, statErr := os.Lstat(snapshot.path)
	if !snapshot.exists {
		if errors.Is(statErr, os.ErrNotExist) {
			return nil
		}
		if statErr != nil {
			return statErr
		}
		return removeSnapshotPath(snapshot.path, info)
	}
	if snapshot.directory {
		if statErr == nil {
			if err := removeSnapshotPath(snapshot.path, info); err != nil {
				return err
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		if err := os.MkdirAll(snapshot.path, snapshot.mode.Perm()); err != nil {
			return err
		}
		for _, child := range snapshot.children {
			if err := restoreSnapshotNode(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(snapshot.path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(snapshot.path, snapshot.content, snapshot.mode.Perm())
}

func removeSnapshotPath(path string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return os.Remove(path)
	}
	return os.RemoveAll(path)
}

func validateTextEdits(content []byte, edits []textEdit) error {
	for _, edit := range edits {
		if edit.InsertTextFormat == 2 {
			return errors.New("snippet-formatted LSP edits are unsupported")
		}
		if _, _, err := rangeByteOffsets(content, edit.Range); err != nil {
			return err
		}
	}
	return nil
}

func applyTextEdits(content []byte, edits []textEdit) ([]byte, error) {
	type offsetEdit struct {
		start int
		end   int
		text  string
		index int
	}
	offsets := make([]offsetEdit, 0, len(edits))
	for index, edit := range edits {
		if edit.InsertTextFormat == 2 {
			return nil, errors.New("snippet-formatted LSP edits are unsupported")
		}
		start, end, err := rangeByteOffsets(content, edit.Range)
		if err != nil {
			return nil, err
		}
		offsets = append(offsets, offsetEdit{start: start, end: end, text: edit.NewText, index: index})
	}
	// Apply from the end so offsets remain valid. Reject overlapping edits,
	// which otherwise make the result server-order dependent.
	sort.SliceStable(offsets, func(i, j int) bool {
		if offsets[i].start != offsets[j].start {
			return offsets[i].start > offsets[j].start
		}
		if offsets[i].end != offsets[j].end {
			return offsets[i].end > offsets[j].end
		}
		// Equal-position insertions are applied in reverse input order so the
		// final document preserves the server's declared insertion order.
		return offsets[i].index > offsets[j].index
	})
	unique := offsets[:0]
	for _, edit := range offsets {
		if len(unique) > 0 {
			previous := unique[len(unique)-1]
			if edit.start == previous.start && edit.end == previous.end && edit.text == previous.text && edit.start != edit.end {
				continue
			}
		}
		unique = append(unique, edit)
	}
	offsets = unique
	for index := 0; index < len(offsets)-1; index++ {
		if offsets[index].start < offsets[index+1].end {
			return nil, errors.New("overlapping text edits")
		}
	}
	result := append([]byte(nil), content...)
	for _, edit := range offsets {
		next := make([]byte, 0, len(result)-(edit.end-edit.start)+len(edit.text))
		next = append(next, result[:edit.start]...)
		next = append(next, edit.text...)
		next = append(next, result[edit.end:]...)
		result = next
	}
	return result, nil
}

func rangeByteOffsets(content []byte, value lspRange) (int, int, error) {
	start, err := positionByteOffset(content, value.Start)
	if err != nil {
		return 0, 0, err
	}
	end, err := positionByteOffset(content, value.End)
	if err != nil {
		return 0, 0, err
	}
	if end < start {
		return 0, 0, errors.New("text edit range ends before it starts")
	}
	return start, end, nil
}

func rangeByteOffsetsForDisplay(content []byte, value lspRange) (int, int, error) {
	return rangeByteOffsets(content, value)
}

func positionByteOffset(content []byte, value position) (int, error) {
	if value.Line < 0 || value.Character < 0 {
		return 0, errors.New("LSP position must be non-negative")
	}
	lineStart := 0
	line := 0
	for line < value.Line {
		index := indexByte(content[lineStart:], '\n')
		if index < 0 {
			return 0, fmt.Errorf("LSP line %d is outside the document", value.Line)
		}
		lineStart += index + 1
		line++
	}
	lineEnd := len(content)
	if index := indexByte(content[lineStart:], '\n'); index >= 0 {
		lineEnd = lineStart + index
	}
	lineBytes := content[lineStart:lineEnd]
	if len(lineBytes) > 0 && lineBytes[len(lineBytes)-1] == '\r' {
		lineBytes = lineBytes[:len(lineBytes)-1]
	}
	column := 0
	byteOffset := 0
	for byteOffset < len(lineBytes) && column < value.Character {
		runeValue, size := utf8.DecodeRune(lineBytes[byteOffset:])
		if runeValue == utf8.RuneError && size == 1 {
			return 0, errors.New("document contains invalid UTF-8")
		}
		byteOffset += size
		if runeValue > 0xffff {
			column += 2
		} else {
			column++
		}
		if column > value.Character {
			return 0, fmt.Errorf("LSP character %d splits a UTF-16 surrogate pair", value.Character)
		}
	}
	if column != value.Character {
		return 0, fmt.Errorf("LSP character %d is outside line %d", value.Character, value.Line)
	}
	return lineStart + byteOffset, nil
}

func indexByte(value []byte, target byte) int {
	for index, item := range value {
		if item == target {
			return index
		}
	}
	return -1
}

func atomicWrite(path string, content []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".banka-lsp-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(info.Mode().Perm()); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, path)
}

func applyResourceOperation(root string, operation resourceOperation, resolver func(string) (string, error)) error {
	options := resourceOperationOptions{}
	if len(operation.Options) > 0 {
		if err := json.Unmarshal(operation.Options, &options); err != nil {
			return fmt.Errorf("decode %s operation options: %w", operation.Kind, err)
		}
	}
	switch operation.Kind {
	case "create":
		path, err := resolveEditPath(root, operation.URI, resolver)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if _, statErr := os.Lstat(path); statErr == nil {
			if options.Overwrite {
				return os.WriteFile(path, nil, 0o644)
			}
			if options.IgnoreIfExists {
				return nil
			}
			return fmt.Errorf("workspace create target already exists: %s", path)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		_ = file.Close()
	case "rename":
		oldPath, err := resolveEditPath(root, operation.OldURI, resolver)
		if err != nil {
			return err
		}
		newPath, err := resolveEditPath(root, operation.NewURI, resolver)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(newPath), 0o755); err != nil {
			return err
		}
		var displacedDirectory string
		var displacedPath string
		if destinationInfo, statErr := os.Lstat(newPath); statErr == nil {
			if !options.Overwrite {
				if options.IgnoreIfExists {
					return nil
				}
				return fmt.Errorf("workspace rename target already exists: %s", newPath)
			}
			sourceInfo, sourceErr := os.Lstat(oldPath)
			if sourceErr != nil {
				return sourceErr
			}
			if !os.SameFile(sourceInfo, destinationInfo) {
				displacedDirectory, err = os.MkdirTemp(filepath.Dir(newPath), ".banka-lsp-displaced-*")
				if err != nil {
					return err
				}
				displacedPath = filepath.Join(displacedDirectory, filepath.Base(newPath))
				if err := os.Rename(newPath, displacedPath); err != nil {
					_ = os.Remove(displacedDirectory)
					return err
				}
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		if err := os.Rename(oldPath, newPath); err != nil {
			if displacedPath != "" {
				_ = os.Rename(displacedPath, newPath)
				_ = os.Remove(displacedDirectory)
			}
			return err
		}
		if displacedDirectory != "" {
			if err := os.RemoveAll(displacedDirectory); err != nil {
				return fmt.Errorf("remove displaced LSP rename target: %w", err)
			}
		}
	case "delete":
		path, err := resolveEditPath(root, operation.URI, resolver)
		if err != nil {
			return err
		}
		if options.Recursive {
			if _, statErr := os.Lstat(path); statErr != nil {
				if errors.Is(statErr, os.ErrNotExist) && options.IgnoreIfNotExists {
					return nil
				}
				return statErr
			}
			return os.RemoveAll(path)
		}
		if err := os.Remove(path); err != nil {
			if errors.Is(err, os.ErrNotExist) && options.IgnoreIfNotExists {
				return nil
			}
			return err
		}
	default:
		return fmt.Errorf("unsupported workspace resource operation: %s", operation.Kind)
	}
	return nil
}

func uriToPath(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("invalid LSP URI %q: %w", value, err)
	}
	if !strings.EqualFold(parsed.Scheme, "file") {
		return "", fmt.Errorf("LSP URI must use file scheme: %s", value)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil || parsed.Port() != "" {
		return "", fmt.Errorf("LSP file URI contains unsupported authority or suffix: %s", value)
	}
	// url.Parse has already unescaped Path exactly once. Calling PathUnescape on
	// it again would turn a literal filename such as "%2F" into a separator.
	path := parsed.Path
	if path == "" && parsed.Opaque != "" {
		path, err = url.PathUnescape(parsed.Opaque)
		if err != nil {
			return "", err
		}
	}
	if parsed.Host != "" && !strings.EqualFold(parsed.Host, "localhost") {
		path = "//" + parsed.Host + path
	}
	if path == "" {
		return "", errors.New("LSP URI contains an empty path")
	}
	if strings.IndexByte(path, 0) >= 0 {
		return "", errors.New("LSP URI contains a NUL byte")
	}
	// RFC 8089 represents Windows drive paths as /C:/path. Strip the URI-only
	// leading slash when running on Windows so fileURI and uriToPath round-trip.
	if filepath.Separator == '\\' && len(path) >= 3 && path[0] == '/' && ((path[1] >= 'A' && path[1] <= 'Z') || (path[1] >= 'a' && path[1] <= 'z')) && path[2] == ':' {
		path = path[1:]
	}
	return filepath.FromSlash(path), nil
}

func safeWorkspacePath(root string, target string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	candidate := target
	// Callers commonly pass workspace-relative paths (for example the public
	// Manager API and FileObserver). Resolve those against the LSP workspace,
	// rather than against the process cwd.
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, abs)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("LSP workspace edit escapes workspace: %s", target)
	}
	return tools.ResolveSafePath(root, abs)
}
