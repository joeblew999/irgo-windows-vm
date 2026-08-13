package utmvm

import (
	"os"
	"testing"
	"time"
)

// A cut-down catalog with the shape the real one has: the same file repeated
// per edition, which is why parsing must deduplicate.
const testCatalogXML = `<MCT>
  <Catalogs>
    <Catalog version="2.0">
      <PublishedMedia id="" release="">
        <Files>
          <File id="">
            <FileName>26100.4349.250607-1500.ge_release_svc_refresh_CLIENTCONSUMER_RET_A64FRE_en-us.esd</FileName>
            <LanguageCode>en-us</LanguageCode>
            <Language>English (United States)</Language>
            <Edition>Professional</Edition>
            <Architecture>ARM64</Architecture>
            <Size>4523456789</Size>
            <Sha1>c78fd344e845d3b17cb91c40bf4a856459da1b6c</Sha1>
            <FilePath>http://dl.delivery.mp.microsoft.com/x/a64-en-us.esd</FilePath>
          </File>
          <File id="">
            <FileName>26100.4349.250607-1500.ge_release_svc_refresh_CLIENTCONSUMER_RET_A64FRE_en-us.esd</FileName>
            <LanguageCode>en-us</LanguageCode>
            <Language>English (United States)</Language>
            <Edition>Core</Edition>
            <Architecture>ARM64</Architecture>
            <Size>4523456789</Size>
            <Sha1>c78fd344e845d3b17cb91c40bf4a856459da1b6c</Sha1>
            <FilePath>http://dl.delivery.mp.microsoft.com/x/a64-en-us.esd</FilePath>
          </File>
          <File id="">
            <FileName>26100.4349.250607-1500.ge_release_svc_refresh_CLIENTCONSUMER_RET_x64FRE_en-us.esd</FileName>
            <LanguageCode>en-us</LanguageCode>
            <Language>English (United States)</Language>
            <Edition>Professional</Edition>
            <Architecture>x64</Architecture>
            <Size>4600950211</Size>
            <Sha1>702d814c27ade4aae380088b1e78b228b7325ee4</Sha1>
            <FilePath>http://dl.delivery.mp.microsoft.com/x/x64-en-us.esd</FilePath>
          </File>
          <File id="">
            <FileName>26100.4349.250607-1500.ge_release_svc_refresh_CLIENTCONSUMER_RET_A64FRE_fr-fr.esd</FileName>
            <LanguageCode>fr-fr</LanguageCode>
            <Language>French</Language>
            <Edition>Professional</Edition>
            <Architecture>ARM64</Architecture>
            <Size>4511111111</Size>
            <Sha1>aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa</Sha1>
            <FilePath>http://dl.delivery.mp.microsoft.com/x/a64-fr-fr.esd</FilePath>
          </File>
        </Files>
      </PublishedMedia>
    </Catalog>
  </Catalogs>
</MCT>`

// The catalog lists each image once per edition it belongs to — the real one
// has 1978 entries that collapse to 153 images. Without deduplication a listing
// shows the same build twenty times and "how many matched?" stops meaning
// anything, which is what guards the refusal to download an ambiguous match.
func TestParseCatalogDeduplicates(t *testing.T) {
	all, err := ISOParseCatalog([]byte(testCatalogXML))
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("got %d unique images, want 3 (four entries, one repeated edition)", len(all))
	}
}

func TestFilterCatalog(t *testing.T) {
	all, err := ISOParseCatalog([]byte(testCatalogXML))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name              string
		arch, lang, edtn  string
		want              int
		wantFileSubstring string
	}{
		{"what this project needs", "ARM64", "en-us", "CLIENTCONSUMER", 1, "A64FRE_en-us"},
		{"arch alone is not enough", "ARM64", "", "", 2, ""},
		{"x64 is a different image", "x64", "en-us", "", 1, "x64FRE"},
		{"no match at all", "ARM64", "de-de", "", 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ISOFilterCatalog(all, tc.arch, tc.lang, tc.edtn)
			if len(got) != tc.want {
				t.Fatalf("got %d, want %d", len(got), tc.want)
			}
			if tc.wantFileSubstring != "" && !contains(got[0].FileName, tc.wantFileSubstring) {
				t.Errorf("matched %q, want one containing %q", got[0].FileName, tc.wantFileSubstring)
			}
		})
	}
}

// Build is how one release is told from another, and there is no field for it —
// only the filename.
func TestCatalogEntryBuild(t *testing.T) {
	all, _ := ISOParseCatalog([]byte(testCatalogXML))
	const want = "26100.4349.250607-1500"
	if got := all[0].Build(); got != want {
		t.Errorf("Build() = %q, want %q", got, want)
	}
	if got := (ISOCatalogEntry{FileName: "nonsense"}).Build(); got != "" {
		t.Errorf("Build() on a malformed name = %q, want empty", got)
	}
}

// The wrong fwlink is a failure that looks like success: LinkId=841361 is the
// WINDOWS 10 catalog and parses perfectly. Asserting they differ is cheap
// insurance against somebody "tidying" the constant.
func TestCatalogURLIsWindows11(t *testing.T) {
	if ISOCatalogURL == ISOCatalogURLWindows10 {
		t.Fatal("ISOCatalogURL points at the Windows 10 catalog")
	}
	if !contains(ISOCatalogURL, "2156292") {
		t.Errorf("ISOCatalogURL = %q, expected the Windows 11 fwlink 2156292", ISOCatalogURL)
	}
}

// TestFetchCatalogLive is the only check that the CAB actually decompresses,
// because the compression Microsoft ships is not something we can fixture
// meaningfully — the point is what THEY serve today, not what they served when
// a test file was captured.
//
// Skipped by default: it needs the network. IRGO_TEST_NETWORK=1 runs it.
func TestFetchCatalogLive(t *testing.T) {
	if os.Getenv("IRGO_TEST_NETWORK") == "" {
		t.Skip("set IRGO_TEST_NETWORK=1 to fetch the real catalog")
	}
	all, err := ISOFetchCatalog(2 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) < 50 {
		t.Fatalf("catalog has %d images, which is too few to be the real one", len(all))
	}

	match := ISOFilterCatalog(all, "ARM64", "en-us", "CLIENTCONSUMER")
	if len(match) != 1 {
		t.Fatalf("ARM64/en-us/consumer matched %d images, want exactly 1 — "+
			"more than one means the filter no longer identifies a single download", len(match))
	}
	e := match[0]
	if len(e.Sha1) != 40 {
		t.Errorf("sha1 = %q, want 40 hex characters", e.Sha1)
	}
	if e.Size < 3<<30 {
		t.Errorf("size = %d, too small to be Windows media", e.Size)
	}
	if !contains(e.FilePath, "dl.delivery.mp.microsoft.com") {
		t.Errorf("download URL = %q, expected Microsoft's delivery host", e.FilePath)
	}
	// Catches the Windows 10 fwlink, which parses fine and is entirely wrong.
	if !contains(e.FileName, "A64FRE") {
		t.Errorf("filename = %q, expected an ARM64 (A64FRE) image", e.FileName)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
