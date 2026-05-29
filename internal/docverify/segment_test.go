package docverify

import "testing"

func TestMergeRegions(t *testing.T) {
	// Deux blobs verticalement proches (gap 20 < mergeGap 50) → fusionnés
	// (cas photo + zone texte d'une même CNI).
	close := []Region{
		{X: 0, Y: 0, Width: 50, Height: 100, Area: 5000},
		{X: 0, Y: 120, Width: 50, Height: 100, Area: 5000},
	}
	if got := mergeRegions(close, 50); len(got) != 1 {
		t.Errorf("régions proches: %d région(s), attendu 1 (fusion)", len(got))
	}

	// Deux blobs éloignés (gap 200 > mergeGap 50) → séparés
	// (cas recto + verso de CNI sur une A4).
	far := []Region{
		{X: 0, Y: 0, Width: 50, Height: 100, Area: 5000},
		{X: 0, Y: 300, Width: 50, Height: 100, Area: 5000},
	}
	if got := mergeRegions(far, 50); len(got) != 2 {
		t.Errorf("régions éloignées: %d région(s), attendu 2 (pas de fusion)", len(got))
	}

	// Une seule région → renvoyée telle quelle
	if got := mergeRegions([]Region{{Y: 0, Height: 100}}, 50); len(got) != 1 {
		t.Errorf("région unique: %d, attendu 1", len(got))
	}
}
