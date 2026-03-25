package fab

import "testing"

func TestParseDeadlineInfo(t *testing.T) {
	info := ParseDeadlineInfo("Limited-Time Free (Until Sep 9, 2025 at 9:59 AM ET)")
	if info == nil {
		t.Fatal("expected deadline info")
	}
	if info.Day != 9 || info.Month != 9 || info.Year != 2025 || info.Hour != 9 || info.Minute != 59 || info.GMTOffset == "" {
		t.Fatalf("unexpected deadline info: %#v", info)
	}
}

func TestParseFreeAssets(t *testing.T) {
	html := `
<html>
  <body>
    <section>
      <h2>Limited-Time Free (Until Sep 9, 2025 at 9:59 AM ET)</h2>
      <ul>
        <li>
          <a href="/listings/abc">Asset One</a>
          <img src="/images/one.png">
        </li>
        <li>
          <a href="/listings/def" aria-label="Asset Two"></a>
          <img src="//cdn.example.com/two.png">
        </li>
      </ul>
    </section>
  </body>
</html>`

	result, err := ParseFreeAssets(html)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Found {
		t.Fatal("expected section to be found")
	}
	if len(result.Assets) != 2 {
		t.Fatalf("expected 2 assets, got %d", len(result.Assets))
	}
	if result.Assets[0].Link != "https://www.fab.com/listings/abc" || result.Assets[0].Image != "https://www.fab.com/images/one.png" {
		t.Fatalf("unexpected first asset: %#v", result.Assets[0])
	}
	if result.Assets[1].Name != "Asset Two" || result.Assets[1].Image != "https://cdn.example.com/two.png" {
		t.Fatalf("unexpected second asset: %#v", result.Assets[1])
	}
	if result.Deadline == nil {
		t.Fatal("expected deadline info")
	}
}

func TestParseFreeAssetsEmptySection(t *testing.T) {
	html := `<section><h2>Limited-Time Free (Until Sep 9 at 9:59 AM ET)</h2></section>`
	result, err := ParseFreeAssets(html)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Found {
		t.Fatal("expected section to be found")
	}
	if len(result.Assets) != 0 {
		t.Fatalf("expected empty assets, got %d", len(result.Assets))
	}
}

func TestParseFreeAssetsMissingSection(t *testing.T) {
	result, err := ParseFreeAssets(`<html><body><section><h2>Other</h2></section></body></html>`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Found {
		t.Fatal("expected missing section")
	}
}

