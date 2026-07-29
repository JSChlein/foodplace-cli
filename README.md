# foodplace

A tiny command-line tool that prints the weekly lunch menu from
[The Food Place](https://salling.thefoodplace.dk) — grouped by day, showing the
**Go Green**, **Deli** and **Comfort Food** dishes.

```
$ foodplace

=== Uge 31 ===

Mandag (2026-07-27)
  Go Green:    Wrap med syltet kål, falafler, hummus og chillimayonnaise
  Deli:        Pålægsfade
  Comfort Food: Boller i karry med hvide ris

Tirsdag (2026-07-28)
  Go Green:    Pasta i oste/flødesauce med aubergine, ærter, chili, trøffel og salvie
  Deli:        Pålægsfade
  Comfort Food: Fettuccine med ragout af okse med parmesan og grønt

Onsdag (2026-07-29)
  Go Green:    Spinat og ricotta lasagne med let marineret urtesalat
  Deli:        Pålægsfade
  Comfort Food: Tyrkisk fladbrød med spinat, kebab og feta

Torsdag (2026-07-30)
  Go Green:    Økologiske kartofler stegt i karrysmør med bagte falafler, edamamebønner og yoghurtdressing
  Deli:        Pålægsfade
  Comfort Food: Kold kartoffelsalat med ristet frankfurter

Fredag (2026-07-31)
  Go Green:    Vegetarisk moussaka med tzatziki
  Deli:        Pålægsfade
  Comfort Food: Tarteletter med høns i asparges

=== Uge 32 ===

Mandag (2026-08-03)
  Go Green:    Pasta i rød pestosauce med bagte tomater, grillet peberfrugt, parmesan og basilikum
  Deli:        Pålægsfade
  Comfort Food: Pasta med krydret tomatsauce og kødboller med parmesan og basilikum

Tirsdag (2026-08-04)
  Go Green:    Vegetarisk korma med blomkål og kikærter
  Deli:        Pålægsfade
  Comfort Food: Kylling i sur/sødsauce med basmatiris

Onsdag (2026-08-05)
  Go Green:    Pasta med grillede tomater, squash og kikærter i cremet citronsauce
  Deli:        Pålægsfade
  Comfort Food: Skinkeschnitzel med smørdampede ærter, kartofler og fløde/paprikasauce

Torsdag (2026-08-06)
  Go Green:    Saml-selv Wrap med tomatsalsa, tzatziki, kikærter med ras el hanout og feta
  Deli:        Pålægsfade
  Comfort Food: Oksefilet med bagt kartoffel, kryddersmør cole slaw

Fredag (2026-08-07)
  Go Green:    Hoisin nudler med bok choy
  Deli:        Pålægsfade
  Comfort Food: Frikadeller med hvide kartofler, skysauce lun rødkål og asier3...
```

## Install

You do **not** need Go installed — the tool ships as a single prebuilt binary.

### macOS / Linux (one-liner)

```sh
curl -fsSL https://raw.githubusercontent.com/JSChlein/foodplace-cli/main/install.sh | sh
```

This downloads the correct binary for your OS/architecture from the latest
[release](https://github.com/JSChlein/foodplace-cli/releases) and installs it to
`/usr/local/bin` (falling back to `~/.local/bin` if that isn't writable).

To pin a version or change the install location:

```sh
VERSION=v1.0.0 INSTALL_DIR="$HOME/bin" \
  sh -c "$(curl -fsSL https://raw.githubusercontent.com/JSChlein/foodplace-cli/main/install.sh)"
```

### Manual download

Grab the archive for your platform from the
[Releases page](https://github.com/JSChlein/foodplace-cli/releases), extract it,
and move the `foodplace` binary somewhere on your `PATH`:

```sh
tar -xzf foodplace_v1.0.0_darwin_arm64.tar.gz
sudo mv foodplace_v1.0.0_darwin_arm64/foodplace /usr/local/bin/
```

**Windows:** download the `..._windows_amd64.zip`, extract `foodplace.exe`, and
place it in a folder on your `PATH`.

> macOS may warn about an unidentified developer the first time you run a
> downloaded binary. Allow it with:
> `xcode-select --version >/dev/null; xattr -d com.apple.quarantine ./foodplace`

### With Go (if you already have it)

```sh
go install github.com/JSChlein/foodplace-cli@latest
```

## Usage

```
foodplace [flags]

  -lang da|en    menu language (default: da)
  -location N    location / banner id (default: 1)
  -week N        show only this ISO week number (default: 0 = all weeks returned)
  -version       print version and exit
```

Examples:

```sh
foodplace                 # this/next week's menu in Danish
foodplace -lang en        # in English
foodplace -week 31        # only ISO week 31
foodplace -location 2     # a different location
```

## Building from source

Only needed if you're developing the tool. Requires [Go](https://go.dev) 1.23+.

```sh
git clone https://github.com/JSChlein/foodplace-cli.git
cd foodplace-cli
go build -o foodplace .      # build for your machine
./build.sh v1.0.0            # cross-compile release archives into ./dist
```

## Releasing

Releases are automated. Push a version tag and GitHub Actions builds binaries
for Linux, macOS and Windows and publishes them to a GitHub Release:

```sh
git tag v1.0.0
git push origin v1.0.0
```

The [`install.sh`](install.sh) one-liner always resolves to the latest release.
