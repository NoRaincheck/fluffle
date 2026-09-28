package tui

import "strings"

// The footer is the keymap, said out loud. It is items rather than one line
// because a run-on line bounded by the narrowest terminal can only afford
// two-letter abbreviations — "g group", "v sort" — which is the same
// information the reader already has and none of what they do not. Items carry
// a description each and are reflowed into as many columns as the terminal
// holds, so the narrowest terminal pays in rows and every wider one buys
// columns back.
type helpItem struct {
	key, description string
}

// helpColumnGap is the complete visible width inserted before every column
// after the first: the divider cell and the one cell after it.
const helpColumnGap = 2

// helpDivider separates two columns of the footer. It is the same cell
// splitlayout uses between panes, so one rule describes every division on
// screen.
const helpDivider = "|"

// helpItems is the whole keymap, in the order handleKey reads it. `q` and
// ctrl+c both quit and both say so: the footer is the only place a key is
// documented, and a key that is not on it is a key the user has to guess at.
//
// The arrow keys are named in the description rather than drawn as U+2191 and
// U+2193, because those are East Asian Ambiguous and a footer that names them
// with glyphs is a footer that wraps on a terminal which renders them wide.
// The keys are still bound and still named; only the spelling changed.
func helpItems() []helpItem {
	return []helpItem{
		{key: "j k", description: "move the cursor, or the arrows"},
		{key: "Enter", description: "read the thread"},
		{key: "Esc", description: "back to the list"},
		{key: "g", description: "group the rows"},
		{key: "v", description: "reverse the order"},
		{key: "r", description: "reply"},
		{key: "q", description: "quit"},
		{key: "ctrl+c", description: "quit"},
	}
}

// helpItemWidth is the cells an item occupies before column alignment. It is
// the wide reading of ambiguous width like every other budget here, so a
// description carrying a curly quote cannot make a footer column one cell
// wider than the space the reflow measured. See cells.go.
func helpItemWidth(it helpItem) int {
	n := wideCells.String(it.key)
	if it.description != "" {
		n += 1 + wideCells.String(it.description)
	}
	return n
}

// helpColumnWidths is the widest item in each column, which is the one grid
// every returned row shares.
func helpColumnWidths(rows [][]helpItem) []int {
	columns := 0
	for _, row := range rows {
		columns = max(columns, len(row))
	}
	widths := make([]int, columns)
	for _, row := range rows {
		for column, it := range row {
			widths[column] = max(widths[column], helpItemWidth(it))
		}
	}
	return widths
}

// reflowHelp lays the items out in the greatest column count that fits
// availableWidth, chunking the input in order so the footer reads in keymap
// order. An item is never truncated: a shortcut cut in half is worse than a
// taller footer, so a width too small for the grid falls back to one item per
// row.
func reflowHelp(items []helpItem, availableWidth, gap int) [][]helpItem {
	if len(items) == 0 {
		return nil
	}
	gap = max(gap, 0)
	for columns := len(items); columns >= 1; columns-- {
		candidate := chunkHelp(items, columns)
		if helpGridWidth(helpColumnWidths(candidate), gap) <= availableWidth {
			return candidate
		}
	}
	return chunkHelp(items, 1)
}

// helpGridWidth is the visible width of one row of the aligned grid: every
// column at its width, and the gap before each column after the first.
func helpGridWidth(widths []int, gap int) int {
	total := gap * (len(widths) - 1)
	for _, w := range widths {
		total += w
	}
	return total
}

func chunkHelp(items []helpItem, columns int) [][]helpItem {
	rows := make([][]helpItem, 0, (len(items)+columns-1)/columns)
	for start := 0; start < len(items); start += columns {
		rows = append(rows, items[start:min(start+columns, len(items))])
	}
	return rows
}

// renderHelp draws the footer at width, one entry per column and the divider
// between columns. Every line is padded to exactly width, so the band under it
// starts at a fixed row however many rows the reflow chose.
func renderHelp(items []helpItem, width int) string {
	if width <= 0 || len(items) == 0 {
		return ""
	}
	rows := reflowHelp(items, width, helpColumnGap)
	widths := helpColumnWidths(rows)
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		cells := make([]string, 0, len(row)*2)
		for column, it := range row {
			// Only a column that holds an item gets a divider in front of it, so
			// a short final row is not padded out with empty columns and rules.
			if column > 0 {
				cells = append(cells, sepStyle.Render(helpDivider)+" ")
			}
			cells = append(cells, pad(
				helpKeyStyle.Render(it.key)+" "+helpDescStyle.Render(it.description),
				widths[column]))
		}
		lines = append(lines, strings.Join(cells, ""))
	}
	return padLines(strings.Join(lines, "\n"), width, len(lines))
}
