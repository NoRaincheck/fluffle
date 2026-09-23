package tui

type layoutMode int

const (
	layoutStacked layoutMode = iota
	layoutSplit
)

const splitMinWidth = 90

func (m model) resolveLayout() layoutMode {
	if m.width >= splitMinWidth {
		return layoutSplit
	}
	return layoutStacked
}

func (m model) splitActive() bool {
	if m.currentView == viewModal {
		return false
	}
	return m.resolveLayout() == layoutSplit
}
