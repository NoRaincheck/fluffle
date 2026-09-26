package agentcfg

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
)

const (
	DefaultReply       = "auto"
	DefaultTimeoutSecs = 300
)

const maxTimeoutSecs = 9223372036

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

type Agent struct {
	Name         string
	Description  string
	Command      string
	Args         []string
	Reply        string
	TimeoutSecs  int
	Env          map[string]string
	SystemPrompt string
}

type Entry struct {
	Agent
	Source string
}

type Set struct {
	byName map[string]Entry
}

func (s *Set) Lookup(name string) (Entry, bool) {
	e, ok := s.byName[name]
	return e, ok
}

func (s *Set) Entries() []Entry {
	out := make([]Entry, 0, len(s.byName))
	for _, e := range s.byName {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

type configFile struct {
	Agents []agentFile `toml:"agents"`
}

type agentFile struct {
	Name         string            `toml:"name"`
	Description  string            `toml:"description"`
	Command      string            `toml:"command"`
	Args         []string          `toml:"args"`
	Reply        string            `toml:"reply"`
	TimeoutSecs  *int              `toml:"timeout_secs"`
	Env          map[string]string `toml:"env"`
	SystemPrompt string            `toml:"system_prompt"`
}

func (f agentFile) agent() Agent {
	a := Agent{
		Name:         f.Name,
		Description:  f.Description,
		Command:      f.Command,
		Args:         f.Args,
		Reply:        f.Reply,
		Env:          f.Env,
		SystemPrompt: f.SystemPrompt,
	}
	if a.Reply == "" {
		a.Reply = DefaultReply
	}
	if f.TimeoutSecs == nil {
		a.TimeoutSecs = DefaultTimeoutSecs
	} else {
		a.TimeoutSecs = *f.TimeoutSecs
	}
	return a
}

func Parse(data []byte, source string) ([]Entry, error) {
	var f configFile
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", source, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, k := range undecoded {
			keys = append(keys, k.String())
		}
		sort.Strings(keys)
		return nil, fmt.Errorf("parse %s: unknown keys: %s", source, strings.Join(keys, ", "))
	}
	out := make([]Entry, 0, len(f.Agents))
	for i, af := range f.Agents {
		a := af.agent()
		if err := validate(a, source, i); err != nil {
			return nil, err
		}
		out = append(out, Entry{Agent: a, Source: source})
	}
	return out, nil
}

func validate(a Agent, source string, i int) error {
	where := fmt.Sprintf("%s: agents[%d]", source, i)
	if !nameRe.MatchString(a.Name) {
		return fmt.Errorf("%s: invalid name %q: must match %s", where, a.Name, nameRe.String())
	}
	if strings.TrimSpace(a.Command) == "" {
		return fmt.Errorf("%s: command required", where)
	}
	switch a.Reply {
	case "stdout", "cli", "auto":
	default:
		return fmt.Errorf("%s: invalid reply %q: want stdout, cli, or auto", where, a.Reply)
	}
	if a.TimeoutSecs <= 0 {
		return fmt.Errorf("%s: invalid timeout_secs %d: must be positive", where, a.TimeoutSecs)
	}
	if a.TimeoutSecs > maxTimeoutSecs {
		return fmt.Errorf("%s: invalid timeout_secs %d: must be at most %d", where, a.TimeoutSecs, maxTimeoutSecs)
	}
	return nil
}

type cached struct {
	entries []Entry
	stamp   time.Time
}

type Loader struct {
	globalPath string
	mu         sync.Mutex
	cache      map[string]cached
}

func NewLoader(globalPath string) *Loader {
	return &Loader{globalPath: globalPath, cache: map[string]cached{}}
}

func (l *Loader) Resolve(repoAbsPath string) (*Set, error) {
	paths := []string{l.globalPath}
	if repoAbsPath != "" {
		paths = append(paths, filepath.Join(repoAbsPath, ".flf.toml"))
	}
	merged := map[string]Entry{}
	for _, p := range paths {
		entries, err := l.load(p)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			e.Args = slices.Clone(e.Args)
			e.Env = maps.Clone(e.Env)
			merged[e.Name] = e
		}
	}
	return &Set{byName: merged}, nil
}

func (l *Loader) load(path string) ([]Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	info, err := os.Stat(path)
	if err != nil {
		delete(l.cache, path)
		return nil, nil
	}
	stamp := info.ModTime()
	if c, ok := l.cache[path]; ok && c.stamp.Equal(stamp) {
		return c.entries, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	entries, err := Parse(data, path)
	if err != nil {
		return nil, err
	}
	l.cache[path] = cached{entries: entries, stamp: stamp}
	return entries, nil
}
