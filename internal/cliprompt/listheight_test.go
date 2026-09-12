package cliprompt

import (
	"strings"
	"testing"

	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
	"github.com/stretchr/testify/require"
)

// A narrow terminal wraps the title onto two rows. Every option must still
// render: the fixed height used to hand one of those rows to the title and
// scroll the last option out of sight.
func TestSelectShowsEveryOptionWhenTitleWraps(t *testing.T) {
	t.Parallel()
	title := promptTitle("Override production with a local env file?", "use arrow keys, enter to select", true)
	labels := []string{"Use stored values", ".env", ".env.coolify", ".env.example", ".env.prod"}
	choices := make([]huh.Option[string], 0, len(labels))
	for _, label := range labels {
		choices = append(choices, huh.NewOption(label, label))
	}
	field := huh.NewSelect[string]().Title(title).Description("Stored environment values remain the default.").Options(choices...)
	field.WithTheme(skaliTheme(true))
	field.WithWidth(60)
	require.Equal(t, 2, wrappedLines(title, 60), "the fixture title must wrap")

	view := field.View()
	for _, label := range labels {
		require.Contains(t, view, label)
	}
	require.Equal(t, 2+1+len(labels), lipgloss.Height(view))
}

func TestMultiSelectShowsEveryOptionWhenTitleWraps(t *testing.T) {
	t.Parallel()
	title := promptTitle("Which capabilities should this node carry in the cluster?", "use arrows and space, enter to confirm", true)
	labels := []string{"app", "db", "s3", "edge", "build"}
	choices := make([]huh.Option[string], 0, len(labels))
	for _, label := range labels {
		choices = append(choices, huh.NewOption(label, label))
	}
	field := &autoHeightMultiSelect{
		MultiSelect: huh.NewMultiSelect[string]().Title(title).Description("Pick at least one.").Options(choices...),
		title:       title,
		description: "Pick at least one.",
		options:     len(labels),
	}
	field.WithTheme(skaliTheme(true))
	field.WithWidth(60)
	require.Equal(t, 2, wrappedLines(title, 60), "the fixture title must wrap")

	view := field.View()
	for _, label := range labels {
		require.Contains(t, view, label)
	}
	require.Equal(t, 2+1+len(labels), lipgloss.Height(view))

	// The wrapper survives the update cycle huh runs on every message.
	model, _ := field.Update(nil)
	_, ok := model.(*autoHeightMultiSelect)
	require.True(t, ok)
}

func TestWrappedLines(t *testing.T) {
	t.Parallel()
	require.Equal(t, 0, wrappedLines("", 40))
	require.Equal(t, 1, wrappedLines("short", 40))
	require.Equal(t, 1, wrappedLines("no width known", 0))
	require.Equal(t, 3, wrappedLines(strings.Repeat("word ", 20), 40))
}
