# mping 🌐

`mping` is a friendly, terminal‑based ping monitor built with Go. It
features a sleek text UI powered by
[Bubble Tea](https://github.com/charmbracelet/bubbletea) and
[Lipgloss](https://github.com/charmbracelet/lipgloss). Use it to watch
multiple hosts in real time, spot outages instantly and keep your
host list tidy.

## ✨ Highlights

- 🗃️ **Loads hosts from a file:** On startup `mping` reads a
  `hosts.txt` file where each line has the format `host,description`.
- 🔁 **Adjustable ping interval:** Hosts are polled on a timer from
  0.5 s up to 5 s. Change the interval at runtime via the options
  dialog.
- ✅ **Colour‑coded status:** Green for reachable hosts, red for
  unreachable ones. A bell sound and a brief highlight draw your
  attention when a host changes status.
- 📏 **Latency & ageing info:** See reply time in milliseconds along
  with when the status last changed and how long ago that was.
- ✍️ **Edit your host list live:** Add, edit or remove entries
  directly in the UI. Saved changes persist to `hosts.txt`.
- ↕️ **Sortable & scrollable:** Sort by name, IP, status, reply time
  or age. Large lists scroll smoothly; the ASCII header and shortcut
  legend stay pinned at the top.

## 🎛️ Controls

| Key | Action                                    |
|---:|-------------------------------------------|
| **A** | Add a new host                          |
| **E** | Edit the selected host                  |
| **D** | Delete the selected host                |
| **S** | Save changes to `hosts.txt`             |
| **R** | Reload hosts from `hosts.txt`           |
| **O** | Options: set interval & sort order      |
| **Q** | Quit `mping`                            |

In dialogs, use **Tab** to cycle between input fields and **Esc** to
cancel.

## 🛠️ Building mping

1. **Install Go ≥ 1.22** if you haven’t already. Get it from
   <https://golang.org/dl/> or via your package manager.
2. **Clone this repository** and enter it:

   ```bash
   git clone https://github.com/your‑username/mping.git
   cd mping
   ```

3. **Download dependencies** using `go mod tidy`:

   ```bash
   go mod tidy
   ```

4. **Compile** the binary:

   ```bash
   go build -o mping
   ```

   To cross‑compile for Linux on another platform, set the appropriate
   `GOOS` and `GOARCH`, e.g.:

   ```bash
   GOOS=linux GOARCH=amd64 go build -o mping
   ```

5. **Prepare your host list:** Create a `hosts.txt` file with one
   host per line, separated by a comma and a short description:

   ```text
   127.0.0.1,Localhost
   google.com,Google
   ```

6. **Run mping**:

   ```bash
   ./mping
   ```

## 🍺 Installation via Homebrew

If you use Homebrew on macOS or Linux, you can install mping directly from our tap instead of building it yourself. First add the tap, then install:

```bash
brew tap teddyfluffkins/homebrew-tap
brew install mping
```

Homebrew will download the latest release binary and link it into your `$PATH`. If a new version is published, you can upgrade via `brew upgrade mping`.

## 🧑‍💻 How it works

mping keeps a list of hosts in memory and periodically spawns
lightweight ping commands to check their reachability. It parses the
response time and updates the table on the fly. The TUI is based on
the Elm architecture: a model holds all state, an update function
reacts to messages (like key presses or new ping results), and a view
function renders the interface. Bubble Tea’s message system drives
the periodic pings and lets the UI stay responsive.

## ⚠️ Permissions

On some systems (macOS in particular) unprivileged users aren’t
allowed to run `ping`. Make sure your user has the necessary rights.
On most Linux distributions, the `ping` binary has the setuid bit
enabled, so you can run it without sudo.

## 📄 License

This project is released under the MIT License. See the
[LICENSE](LICENSE) file for full details.