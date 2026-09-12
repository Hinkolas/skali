package cliprompt

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
)

// Neither list field is given a fixed height. Huh sizes a select's viewport
// to its options when no height is set, and the form clamps the field to the
// terminal when the window is too short, so every choice stays visible and
// scrolling only appears when it cannot be avoided. A fixed height counted
// the title as one row and lost an option whenever the title wrapped in a
// narrow terminal, with no hint that the list scrolled.
//
// Huh's multi-select gets that wrong on its own: with no height set it
// subtracts the title and description rows from the option count and hides
// that many choices. autoHeightMultiSelect tells it the full height at the
// width the form hands down, once the wrapped title height is known.
type autoHeightMultiSelect struct {
	*huh.MultiSelect[string]
	title, description string
	options            int
}

func (f *autoHeightMultiSelect) WithWidth(width int) huh.Field {
	f.MultiSelect.WithWidth(width)
	f.MultiSelect.Height(f.options + wrappedLines(f.title, width) + wrappedLines(f.description, width))
	return f
}

// Update keeps the wrapper in the form: huh stores the model a field's
// Update returns in place of the field.
func (f *autoHeightMultiSelect) Update(msg tea.Msg) (huh.Model, tea.Cmd) {
	model, cmd := f.MultiSelect.Update(msg)
	if inner, ok := model.(*huh.MultiSelect[string]); ok {
		f.MultiSelect = inner
	}
	return f, cmd
}

// wrappedLines counts the rows text takes at width, wrapping the way huh
// wraps titles and descriptions. The skali base style carries no horizontal
// frame, so the field width is the text width.
func wrappedLines(text string, width int) int {
	if text == "" {
		return 0
	}
	if width <= 0 {
		return lipgloss.Height(text)
	}
	return lipgloss.Height(lipgloss.Wrap(text, width, ",.-; "))
}
