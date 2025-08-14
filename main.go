package main

import (
    "bufio"
    "context"
    "fmt"
    "os"
    "os/exec"
    "runtime"
    "sort"
    "strconv"
    "strings"
    "sync"
    "time"

    "net"

    "github.com/charmbracelet/bubbles/textinput"
    "github.com/charmbracelet/lipgloss"
    tea "github.com/charmbracelet/bubbletea"
    figure "github.com/common-nighthawk/go-figure"
)

// Host represents a single ping target along with a description.
type Host struct {
    Host string
    Desc string
}

// pingResult holds the outcome of pinging a host. A negative reply means the host
// did not respond within the timeout.
// pingResult represents the current state of a host. In addition to whether
// the host responded and the round‑trip time, it records when the status
// last changed. A zero time indicates the status has never been evaluated.
type pingResult struct {
    status     bool
    reply      float64
    lastChange time.Time
    flashUntil time.Time
}

// pingResultsMsg is sent to the update loop containing the results for all
// hosts. The order of the slice corresponds to the order of the hosts slice.
type pingResultsMsg []pingResult

// tickMsg signals it's time to perform the next round of pings.
type tickMsg time.Time

// Available sort options for the host list. The first element corresponds
// to alphabetical sorting by host name; the second sorts by resolved IP
// address.
var sortChoices = []string{"name", "ip", "status", "reply", "age"}

// modelMode enumerates the various high‑level states the TUI can be in.
type modelMode int

const (
    modeList modelMode = iota
    modeAdd
    modeEdit
    modeConfirmDelete
    modeOptions
)

// model encapsulates all state for the bubbletea program.
type model struct {
    hosts   []Host      // loaded hosts, sorted by hostname
    results []pingResult // current status for each host
    cursor  int          // selected row in table
    width   int          // width of the terminal
    height  int          // height of the terminal
    mode    modelMode    // current UI mode

    // fields used during add/edit operations
    inputHost textinput.Model
    inputDesc textinput.Model
    editIndex int         // index being edited
    confirmIndex int      // index being confirmed for deletion

    message    string      // temporary message displayed at bottom of table

    interval time.Duration // ping interval
    quitting bool          // indicates program should quit

    // Sorting preference: "name" or "ip". Determines how hosts are ordered.
    sortBy string

    // Fields used for the options dialog
    optInterval textinput.Model
    // In options mode we present a small list of sort choices rather than a text input.
    optSortIndex int  // index into optSortChoices
    optFocus     int  // 0 for interval input, 1 for sort selection
}

// loadHostsFromFile reads hosts from hosts.txt. Each line should have the form
// "host,description". Blank lines are ignored. The returned slice is sorted
// alphabetically by host.
func loadHostsFromFile(path string) ([]Host, error) {
    file, err := os.Open(path)
    if err != nil {
        return nil, err
    }
    defer file.Close()
    var hosts []Host
    scanner := bufio.NewScanner(file)
    for scanner.Scan() {
        line := strings.TrimSpace(scanner.Text())
        if line == "" {
            continue
        }
        parts := strings.SplitN(line, ",", 2)
        host := strings.TrimSpace(parts[0])
        desc := ""
        if len(parts) > 1 {
            desc = strings.TrimSpace(parts[1])
        }
        if host != "" {
            hosts = append(hosts, Host{Host: host, Desc: desc})
        }
    }
    if err := scanner.Err(); err != nil {
        return nil, err
    }
    sort.Slice(hosts, func(i, j int) bool { return strings.ToLower(hosts[i].Host) < strings.ToLower(hosts[j].Host) })
    return hosts, nil
}

// saveHostsToFile writes the hosts slice back to hosts.txt. Each line is
// formatted as "host,description". Existing file content will be replaced.
func saveHostsToFile(path string, hosts []Host) error {
    f, err := os.Create(path)
    if err != nil {
        return err
    }
    defer f.Close()
    writer := bufio.NewWriter(f)
    for i, h := range hosts {
        if i > 0 {
            if _, err := writer.WriteString("\n"); err != nil {
                return err
            }
        }
        line := h.Host
        if h.Desc != "" {
            line += "," + h.Desc
        }
        if _, err := writer.WriteString(line); err != nil {
            return err
        }
    }
    return writer.Flush()
}

// sortHosts orders the hosts slice according to the sortBy field. If sortBy
// is "ip", hosts are sorted by their resolved IP address (string
// comparison). If sortBy is anything else, hosts are sorted by hostname
// alphabetically.
func (m *model) sortHosts() {
    // Build an index slice representing the current ordering
    n := len(m.hosts)
    idx := make([]int, n)
    for i := 0; i < n; i++ {
        idx[i] = i
    }
    // Sort the indices according to the chosen criterion. Use stable sort
    // semantics so that equal elements retain relative order.
    sort.SliceStable(idx, func(a, b int) bool {
        i, j := idx[a], idx[b]
        switch m.sortBy {
        case "ip":
            ipA := m.hosts[i].Host
            ipB := m.hosts[j].Host
            if addrs, err := net.LookupIP(m.hosts[i].Host); err == nil && len(addrs) > 0 {
                ipA = addrs[0].String()
            }
            if addrs, err := net.LookupIP(m.hosts[j].Host); err == nil && len(addrs) > 0 {
                ipB = addrs[0].String()
            }
            return ipA < ipB
        case "status":
            // Show reachable hosts first; if both same, fallback to name
            var statusA, statusB bool
            if i < len(m.results) {
                statusA = m.results[i].status
            }
            if j < len(m.results) {
                statusB = m.results[j].status
            }
            if statusA != statusB {
                return statusA && !statusB
            }
            return strings.ToLower(m.hosts[i].Host) < strings.ToLower(m.hosts[j].Host)
        case "reply":
            // Sort by reply time ascending; unreachable (reply <0) go to bottom
            var rA, rB float64 = 1e9, 1e9
            if i < len(m.results) {
                if m.results[i].status {
                    rA = m.results[i].reply
                }
            }
            if j < len(m.results) {
                if m.results[j].status {
                    rB = m.results[j].reply
                }
            }
            if rA != rB {
                return rA < rB
            }
            return strings.ToLower(m.hosts[i].Host) < strings.ToLower(m.hosts[j].Host)
        case "age":
            // Sort by age descending (largest age first)
            ageA := 0.0
            ageB := 0.0
            if i < len(m.results) {
                if !m.results[i].lastChange.IsZero() {
                    ageA = time.Since(m.results[i].lastChange).Seconds()
                }
            }
            if j < len(m.results) {
                if !m.results[j].lastChange.IsZero() {
                    ageB = time.Since(m.results[j].lastChange).Seconds()
                }
            }
            if ageA != ageB {
                return ageA > ageB
            }
            return strings.ToLower(m.hosts[i].Host) < strings.ToLower(m.hosts[j].Host)
        default:
            // "name" or unknown
            return strings.ToLower(m.hosts[i].Host) < strings.ToLower(m.hosts[j].Host)
        }
    })
    // Apply the sorted order to hosts and results
    newHosts := make([]Host, n)
    newResults := make([]pingResult, len(m.results))
    for k, original := range idx {
        newHosts[k] = m.hosts[original]
        if original < len(m.results) {
            newResults[k] = m.results[original]
        }
    }
    m.hosts = newHosts
    m.results = newResults
}

// pingHost attempts to ping a host once. It returns whether the host is up
// and, if up, the round‑trip time in milliseconds. For non‑Windows systems it
// relies on the system ping command with a count of 1. A context with timeout
// is used to enforce an upper bound on execution time. On any error or
// timeout, the host is considered down and the reply time is set to -1.
func pingHost(host string) (bool, float64) {
    var args []string
    if runtime.GOOS == "windows" {
        // On Windows: -n <count>, -w <timeout_ms>
        args = []string{"-n", "1", "-w", "1000", host}
    } else {
        // On Unix/Mac: -c <count>. We'll rely on the context timeout to kill
        // the process if it takes too long.
        args = []string{"-c", "1", host}
    }
    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()
    out, err := exec.CommandContext(ctx, "ping", args...).CombinedOutput()
    if err != nil && ctx.Err() == context.DeadlineExceeded {
        return false, -1
    }
    // Determine success by looking for TTL in output. Different platforms
    // capitalise TTL differently.
    outStr := string(out)
    if strings.Contains(strings.ToLower(outStr), "ttl=") {
        // Attempt to extract the time using a simple substring search.
        // The output usually contains "time=XX ms" or "time<XX ms".
        // We'll search for "time" followed by '=' or '<', then grab the
        // number until the next space.
        idx := strings.Index(outStr, "time")
        if idx != -1 {
            // Move past 'time' and any '=' or '<' characters
            j := idx + len("time")
            for j < len(outStr) && (outStr[j] == '=' || outStr[j] == '<' || outStr[j] == ' ') {
                j++
            }
            // Extract digits and decimal point
            start := j
            for j < len(outStr) && (outStr[j] == '.' || (outStr[j] >= '0' && outStr[j] <= '9')) {
                j++
            }
            if start < j {
                valStr := outStr[start:j]
                if v, err := strconv.ParseFloat(valStr, 64); err == nil {
                    return true, v
                }
            }
        }
        // Host is up but we couldn't parse the time
        return true, -1
    }
    return false, -1
}

// tickCmd returns a command that waits for m.interval before sending a tickMsg.
func (m model) tickCmd() tea.Cmd {
    return tea.Tick(m.interval, func(t time.Time) tea.Msg {
        return tickMsg(t)
    })
}

// pingAllCmd returns a command that concurrently pings all hosts and returns
// the results in a pingResultsMsg.
func pingAllCmd(hosts []Host) tea.Cmd {
    return func() tea.Msg {
        results := make([]pingResult, len(hosts))
        var wg sync.WaitGroup
        for i, h := range hosts {
            wg.Add(1)
            go func(i int, host string) {
                defer wg.Done()
                up, ms := pingHost(host)
                results[i] = pingResult{status: up, reply: ms}
            }(i, h.Host)
        }
        wg.Wait()
        return pingResultsMsg(results)
    }
}

// Init implements tea.Model. It sets up the program by triggering an initial
// ping and requesting a window size. It also starts the periodic tick.
func (m model) Init() tea.Cmd {
    // Start ticking and perform an initial ping. The alt screen is enabled
    // via tea.NewProgram in main().
    return tea.Batch(
        m.tickCmd(),
        pingAllCmd(m.hosts),
    )
}

// setMessage assigns a temporary message to be displayed at the bottom of
// the interface. Currently the message remains until replaced by another
// message. This helper centralises assignment and may be extended later.
func (m *model) setMessage(msg string) {
    m.message = msg
}

// Update implements tea.Model. It handles all incoming messages and updates
// the model accordingly.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case tea.WindowSizeMsg:
        // Capture terminal dimensions
        m.width = msg.Width
        m.height = msg.Height
        return m, nil
    case tickMsg:
        // Schedule a ping
        return m, pingAllCmd(m.hosts)
    case pingResultsMsg:
        // Update statuses and track last change times. Schedule the next tick.
        now := time.Now()
        // Ensure results slice exists and has correct length
        if m.results == nil || len(m.results) != len(msg) {
            m.results = make([]pingResult, len(msg))
        }
        for i, res := range msg {
            prev := m.results[i]
            newRes := pingResult{status: res.status, reply: res.reply, lastChange: prev.lastChange}
            // If this is the first time we've evaluated this host, record now as the
            // last change time.
            if newRes.lastChange.IsZero() {
                newRes.lastChange = now
            }
            // If status flipped, update last change time
            if prev.status != res.status {
                newRes.lastChange = now
                // Highlight the row for a short period and play a beep
                newRes.flashUntil = now.Add(2 * time.Second)
                // Print a bell character to trigger terminal beep
                fmt.Print("\a")
            } else {
                // carry over existing flash window if still active
                if prev.flashUntil.After(now) {
                    newRes.flashUntil = prev.flashUntil
                }
            }
            m.results[i] = newRes
        }
        return m, m.tickCmd()
    case tea.KeyMsg:
        // Global key handling depends on mode
        if m.mode == modeList {
            switch msg.String() {
            case "ctrl+c", "q", "Q":
                m.quitting = true
                return m, tea.Quit
            case "up", "k", "K":
                if m.cursor > 0 {
                    m.cursor--
                }
                return m, nil
            case "down", "j", "J":
                if m.cursor < len(m.hosts)-1 {
                    m.cursor++
                }
                return m, nil
            case "a", "A":
                // Add new host
                m.mode = modeAdd
                m.inputHost = textinput.New()
                m.inputHost.Placeholder = "Host"
                m.inputHost.Focus()
                m.inputDesc = textinput.New()
                m.inputDesc.Placeholder = "Description"
                return m, nil
            case "e", "E":
                if len(m.hosts) == 0 {
                    return m, nil
                }
                // Edit existing host at cursor
                m.mode = modeEdit
                m.editIndex = m.cursor
                m.inputHost = textinput.New()
                m.inputHost.SetValue(m.hosts[m.editIndex].Host)
                m.inputHost.Focus()
                m.inputDesc = textinput.New()
                m.inputDesc.SetValue(m.hosts[m.editIndex].Desc)
                return m, nil
            case "d", "D":
                if len(m.hosts) == 0 {
                    return m, nil
                }
                m.mode = modeConfirmDelete
                m.confirmIndex = m.cursor
                return m, nil
            case "s", "S":
                // Save hosts to file
                if err := saveHostsToFile("hosts.txt", m.hosts); err != nil {
                    m.setMessage("Failed to save: " + err.Error())
                } else {
                    m.setMessage("Hosts saved")
                }
                return m, nil
            case "r", "R":
                // Reload hosts from file
                if h, err := loadHostsFromFile("hosts.txt"); err == nil {
                    m.hosts = h
                    // Reallocate results slice
                    m.results = make([]pingResult, len(m.hosts))
                    // Reset cursor
                    m.cursor = 0
                    m.setMessage("Hosts reloaded")
                    // Sort according to current preference
                    m.sortHosts()
                    return m, pingAllCmd(m.hosts)
                }
                return m, nil
            case "o", "O":
                // Open options dialog
                m.mode = modeOptions
                // Initialize interval input
                m.optInterval = textinput.New()
                m.optInterval.Placeholder = "Interval (0.5–5 s)"
                m.optInterval.SetValue(fmt.Sprintf("%.1f", m.interval.Seconds()))
                m.optInterval.Focus()
                // Determine current sort index
                m.optSortIndex = 0
                for i, choice := range sortChoices {
                    if choice == m.sortBy {
                        m.optSortIndex = i
                        break
                    }
                }
                m.optFocus = 0
                return m, nil
            }
        } else if m.mode == modeAdd || m.mode == modeEdit {
            // When in add/edit mode, delegate key events to focused text input
            var cmd tea.Cmd
            if m.inputHost.Focused() {
                // Tab moves focus to desc field
                if msg.String() == "tab" {
                    m.inputHost.Blur()
                    m.inputDesc.Focus()
                    return m, nil
                }
                // Escape cancels add/edit
                if msg.String() == "esc" {
                    m.mode = modeList
                    return m, nil
                }
                // Enter moves to desc if host is currently focused but empty, or confirms if desc is not focused
                if msg.String() == "enter" {
                    // Move focus to desc field
                    m.inputHost.Blur()
                    m.inputDesc.Focus()
                    return m, nil
                }
                m.inputHost, cmd = m.inputHost.Update(msg)
                return m, cmd
            } else {
                // Desc field is focused
                if msg.String() == "tab" {
                    m.inputDesc.Blur()
                    m.inputHost.Focus()
                    return m, nil
                }
                if msg.String() == "esc" {
                    m.mode = modeList
                    return m, nil
                }
                if msg.String() == "enter" {
                    // Confirm add/edit
                    hostVal := strings.TrimSpace(m.inputHost.Value())
                    descVal := strings.TrimSpace(m.inputDesc.Value())
                    if hostVal == "" {
                        // Do not add/edit if host empty
                        m.setMessage("Host cannot be empty")
                        return m, nil
                    }
                    if m.mode == modeAdd {
                        // Append new host
                        m.hosts = append(m.hosts, Host{Host: hostVal, Desc: descVal})
                    } else if m.mode == modeEdit {
                        // Update existing host
                        if m.editIndex >= 0 && m.editIndex < len(m.hosts) {
                            m.hosts[m.editIndex] = Host{Host: hostVal, Desc: descVal}
                        }
                    }
                    // Sort hosts and reposition cursor to the edited/added host
                    sort.Slice(m.hosts, func(i, j int) bool { return strings.ToLower(m.hosts[i].Host) < strings.ToLower(m.hosts[j].Host) })
                    // Rebuild results slice
                    m.results = make([]pingResult, len(m.hosts))
                    // find index of hostVal
                    m.cursor = 0
                    for i, h := range m.hosts {
                        if h.Host == hostVal {
                            m.cursor = i
                            break
                        }
                    }
                    // Switch back to list mode
                    m.mode = modeList
                    // Trigger ping to update status immediately
                    return m, pingAllCmd(m.hosts)
                }
                m.inputDesc, cmd = m.inputDesc.Update(msg)
                return m, cmd
            }
        } else if m.mode == modeConfirmDelete {
            switch msg.String() {
            case "y", "Y":
                // Delete host at confirmIndex
                if m.confirmIndex >= 0 && m.confirmIndex < len(m.hosts) {
                    m.hosts = append(m.hosts[:m.confirmIndex], m.hosts[m.confirmIndex+1:]...)
                    // Remove corresponding result entry as well
                    if m.confirmIndex < len(m.results) {
                        m.results = append(m.results[:m.confirmIndex], m.results[m.confirmIndex+1:]...)
                    }
                    // Adjust cursor if necessary
                    if m.cursor >= len(m.hosts) && m.cursor > 0 {
                        m.cursor--
                    }
                    m.setMessage("Host deleted")
                }
                m.mode = modeList
                return m, nil
            case "n", "N", "esc":
                // Cancel deletion
                m.mode = modeList
                return m, nil
            }
        } else if m.mode == modeOptions {
            // Options mode: adjust ping interval (float seconds) and sort choice (name/ip).
            var cmd tea.Cmd
            if m.optFocus == 0 {
                // Interval input field has focus
                switch msg.String() {
                case "tab":
                    m.optInterval.Blur()
                    m.optFocus = 1
                    return m, nil
                case "esc":
                    // Cancel options changes
                    m.mode = modeList
                    return m, nil
                case "enter":
                    // Move focus to sort choice
                    m.optInterval.Blur()
                    m.optFocus = 1
                    return m, nil
                }
                m.optInterval, cmd = m.optInterval.Update(msg)
                return m, cmd
            }
            // Sort list is focused
            switch msg.String() {
            case "tab":
                // Return focus to interval input
                m.optFocus = 0
                m.optInterval.Focus()
                return m, nil
            case "esc":
                // Cancel without applying
                m.mode = modeList
                return m, nil
            case "enter":
                // Confirm options
                intervalStr := strings.TrimSpace(m.optInterval.Value())
                // Normalize decimal comma to dot to support locales like de_DE
                normalized := strings.ReplaceAll(intervalStr, ",", ".")
                // Append unit and parse as time.Duration (e.g. "0.5s")
                dur, err := time.ParseDuration(normalized + "s")
                if err != nil {
                    m.setMessage("Invalid interval; please specify seconds between 0.5 and 5")
                    return m, nil
                }
                // Clamp allowed range 0.5s–5s
                sec := dur.Seconds()
                if sec < 0.5 || sec > 5 {
                    m.setMessage("Interval must be between 0.5 and 5 seconds")
                    return m, nil
                }
                sortStr := sortChoices[m.optSortIndex]
                // Apply new settings
                m.interval = dur
                m.sortBy = sortStr
                // Preserve old hosts and results for remapping
                oldHosts := make([]Host, len(m.hosts))
                copy(oldHosts, m.hosts)
                oldResults := make([]pingResult, len(m.results))
                copy(oldResults, m.results)
                // Re-sort hosts according to new preference
                m.sortHosts()
                // Remap old results to the new ordering based on host names
                newResults := make([]pingResult, len(m.hosts))
                for i, h := range m.hosts {
                    for j, oh := range oldHosts {
                        if h.Host == oh.Host && j < len(oldResults) {
                            newResults[i] = oldResults[j]
                            break
                        }
                    }
                }
                m.results = newResults
                m.cursor = 0
                // Exit options mode
                m.mode = modeList
                // Trigger immediate ping to update statuses and apply new interval
                return m, pingAllCmd(m.hosts)
            case "up", "k", "K":
                if m.optSortIndex > 0 {
                    m.optSortIndex--
                }
                return m, nil
            case "down", "j", "J":
                if m.optSortIndex < len(sortChoices)-1 {
                    m.optSortIndex++
                }
                return m, nil
            }
            // Ignore other keys while choosing sort option
            return m, nil
        }
    }
    return m, nil
}

// widthFor computes the column widths for the table. It ensures a minimum
// width for each column based on the header titles. It then extends widths
// based on the longest value in each column.
// widthFor determines the widths for each column of the table. It bases
// widths on the longest content currently in that column, while also
// respecting the header labels. The results slice is consulted for the
// change and age columns. This ensures the table adjusts dynamically as
// runtime values grow.
func widthFor(hosts []Host, results []pingResult) (wHost, wDesc, wStatus, wReply, wChange, wAge int) {
    // Start with header lengths
    wHost = len("HOST")
    wDesc = len("DESC")
    wStatus = len("STATUS")
    wReply = len("REPLY(ms)")
    wChange = len("LAST STATUS CHANGE")
    wAge = len("AGE")
    // Host and description widths
    for _, h := range hosts {
        if l := len(h.Host); l > wHost {
            wHost = l
        }
        if l := len(h.Desc); l > wDesc {
            wDesc = l
        }
    }
    // Status is fixed width of either "UP" or "DOWN". Already handled by header
    // Reply width depends on the numeric value
    for _, res := range results {
        // reply printed with one decimal or '-' -> at least 1 char; we consider string length
        if res.status {
            if res.reply >= 0 {
                s := fmt.Sprintf("%.1f", res.reply)
                if len(s) > wReply {
                    wReply = len(s)
                }
            }
        }
        if !res.lastChange.IsZero() {
            // last change time always formatted as HH:MM:SS (8 chars)
            if 8 > wChange {
                wChange = 8
            }
            // age as number of seconds since last change
            ageStr := fmt.Sprintf("%.0f", time.Since(res.lastChange).Seconds())
            if len(ageStr) > wAge {
                wAge = len(ageStr)
            }
        }
    }
    return
}

// View renders the UI based on the current state. It uses lipgloss to
// center the header, legend and table. Colour is applied to the status
// column to distinguish up/down hosts. The selected row is highlighted.
func (m model) View() string {
    if m.quitting {
        return ""
    }
    // Build ASCII header
    fig := figure.NewFigure("MPING", "", true)
    headerLines := strings.Split(fig.String(), "\n")
    header := ""
    hdrStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Bold(true)
    width := m.width
    if width == 0 {
        width = 80
    }
    centerLine := func(s string) string {
        return lipgloss.NewStyle().Width(width).Align(lipgloss.Center).Render(s)
    }
    for _, line := range headerLines {
        if strings.TrimSpace(line) == "" {
            continue
        }
        header += centerLine(hdrStyle.Render(line)) + "\n"
    }
    // Legend
    legend := "A Add   E Edit   D Delete   S Save   R Reload   O Options   Q Quit"
    legendStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("7")).Bold(true)
    header += centerLine(legendStyle.Render(legend)) + "\n\n"
    // Table column widths
    wHost, wDesc, wStatus, wReply, wChange, wAge := widthFor(m.hosts, m.results)
    // Spacing between columns
    colSep := 2
    // Compose header row
    headerRow := fmt.Sprintf(
        "%-*s%s%-*s%s%-*s%s%*s%s%*s%s%*s",
        wHost, "HOST", strings.Repeat(" ", colSep),
        wDesc, "DESC", strings.Repeat(" ", colSep),
        wStatus, "STATUS", strings.Repeat(" ", colSep),
        wReply, "REPLY(ms)", strings.Repeat(" ", colSep),
        wChange, "LAST STATUS CHANGE", strings.Repeat(" ", colSep),
        wAge, "AGE",
    )
    // Build rows. We'll construct each column separately, pad it to its width
    // and apply colouring and selection styles after. To avoid overflowing
    // the terminal height when many hosts are present, we compute how many
    // rows can fit below the header and message areas.
    var rows []string
    // Styles for statuses
    upStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
    downStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
    // Selection background style (only background colour so that per‑column
    // foreground colouring remains visible)
    selectedBg := lipgloss.NewStyle().Background(lipgloss.Color("4"))
    sep := strings.Repeat(" ", colSep)
    // Compute how many lines the header occupies to estimate available space
    headerLinesCount := len(strings.Split(strings.TrimRight(header, "\n"), "\n"))
    // Reserve two lines for the message (blank + message) if it exists
    extra := 0
    if m.message != "" {
        extra = 2
    }
    // Estimate how many host rows can fit
    // Subtract two additional lines for the blank line after the legend and
    // spacing before the table. This helps ensure the table fits.
    availableRows := m.height - headerLinesCount - extra - 2
    if availableRows < 0 {
        availableRows = 0
    }
    if availableRows > len(m.hosts) {
        availableRows = len(m.hosts)
    }
    // Determine starting index to show so that cursor is visible
    start := 0
    if m.cursor >= availableRows {
        start = m.cursor - availableRows + 1
    }
    if start < 0 {
        start = 0
    }
    end := start + availableRows
    if end > len(m.hosts) {
        end = len(m.hosts)
    }
    for idx := start; idx < end; idx++ {
        h := m.hosts[idx]
        // Determine status and prepare padded plain text for status
        statusPlain := "DOWN"
        reply := "-"
        change := "-"
        age := "-"
        res := pingResult{}
        if idx < len(m.results) {
            res = m.results[idx]
        }
        if res.status {
            statusPlain = "UP"
            if res.reply >= 0 {
                reply = fmt.Sprintf("%.1f", res.reply)
            }
        }
        if !res.lastChange.IsZero() {
            change = res.lastChange.Format("15:04:05")
            age = fmt.Sprintf("%.0f", time.Since(res.lastChange).Seconds())
        }
        // Pad each column
        hostCol := fmt.Sprintf("%-*s", wHost, h.Host)
        descCol := fmt.Sprintf("%-*s", wDesc, h.Desc)
        // Status column padded and then coloured
        statusColPlain := fmt.Sprintf("%-*s", wStatus, statusPlain)
        var statusCol string
        if res.status {
            statusCol = upStyle.Render(statusColPlain)
        } else {
            statusCol = downStyle.Render(statusColPlain)
        }
        replyCol := fmt.Sprintf("%*s", wReply, reply)
        changeCol := fmt.Sprintf("%*s", wChange, change)
        ageCol := fmt.Sprintf("%*s", wAge, age)
        parts := []string{hostCol, descCol, statusCol, replyCol, changeCol, ageCol}
        // Apply flash highlight if status recently changed and this row is not selected
        if idx < len(m.results) {
            res := m.results[idx]
            if res.flashUntil.After(time.Now()) && (m.mode != modeList || idx != m.cursor) {
                // Highlight the row when a status changes by adding a coloured
                // background and bold text to all cells except the status
                // column. This preserves the coloured status text while still
                // drawing the user's attention to the change.
                var fs lipgloss.Style
                if res.status {
                    // Green background for hosts that are now UP.
                    fs = lipgloss.NewStyle().Background(lipgloss.Color("10")).Bold(true)
                } else {
                    // Red background for hosts that went DOWN.
                    fs = lipgloss.NewStyle().Background(lipgloss.Color("1")).Bold(true)
                }
                for j := range parts {
                    // Skip the status column (index 2) to retain its
                    // existing colour.
                    if j == 2 {
                        continue
                    }
                    parts[j] = fs.Render(parts[j])
                }
            }
        }
        // Apply selection background if this row is selected in list mode
        if idx == m.cursor && m.mode == modeList && len(m.hosts) > 0 {
            for j := range parts {
                parts[j] = selectedBg.Render(parts[j])
            }
        }
        line := strings.Join(parts, sep)
        rows = append(rows, centerLine(line))
    }
    // Assemble table string. Header row is centred separately.
    table := centerLine(headerRow) + "\n"
    for _, row := range rows {
        table += row + "\n"
    }
    // Build prompt for add/edit/delete modes
    var overlay string
    if m.mode == modeAdd {
        // Add mode: show two fields and hint
        overlay = "Add new host:\n"
        overlay += "Host: " + m.inputHost.View() + "\n"
        overlay += "Desc: " + m.inputDesc.View() + "\n"
        overlay += "Press Tab to switch, Enter to confirm, Esc to cancel"
    } else if m.mode == modeEdit {
        overlay = "Edit host:\n"
        overlay += "Host: " + m.inputHost.View() + "\n"
        overlay += "Desc: " + m.inputDesc.View() + "\n"
        overlay += "Press Tab to switch, Enter to confirm, Esc to cancel"
    } else if m.mode == modeConfirmDelete {
        if m.confirmIndex >= 0 && m.confirmIndex < len(m.hosts) {
            overlay = fmt.Sprintf("Delete host '%s'? (y/n)", m.hosts[m.confirmIndex].Host)
        }
    } else if m.mode == modeOptions {
        overlay = "Options:\n"
        overlay += "Interval: " + m.optInterval.View() + "\n"
        overlay += "Sort by:\n"
        for i, choice := range sortChoices {
            prefix := "  "
            display := strings.Title(choice)
            if i == m.optSortIndex {
                prefix = "> "
            }
            overlay += prefix + display + "\n"
        }
        overlay += "Press Tab to switch, Up/Down to choose, Enter to confirm, Esc to cancel"
    }
    // Compose final view
    var out strings.Builder
    out.WriteString(header)
    if overlay != "" {
        // When an overlay is present, display it centered and beneath the header
        for _, line := range strings.Split(overlay, "\n") {
            out.WriteString(centerLine(line))
            out.WriteString("\n")
        }
    } else {
        out.WriteString(table)
    }
    // Append message at bottom
    if m.message != "" {
        out.WriteString("\n")
        out.WriteString(centerLine(lipgloss.NewStyle().Foreground(lipgloss.Color("13")).Render(m.message)))
        out.WriteString("\n")
    }
    return out.String()
}

// main entry point: loads hosts, constructs model and runs the TUI.
func main() {
    hosts, err := loadHostsFromFile("hosts.txt")
    if err != nil && !os.IsNotExist(err) {
        fmt.Fprintf(os.Stderr, "Failed to load hosts: %v\n", err)
        os.Exit(1)
    }
    // Initialise default ping results slice
    results := make([]pingResult, len(hosts))
    m := model{
        hosts:    hosts,
        results:  results,
        cursor:   0,
        interval: 5 * time.Second,
        mode:     modeList,
        sortBy:   "name",
    }
    // Ensure initial host list is sorted alphabetically
    m.sortHosts()
    p := tea.NewProgram(m, tea.WithAltScreen())
    if _, err := p.Run(); err != nil {
        fmt.Fprintf(os.Stderr, "Error running program: %v\n", err)
        os.Exit(1)
    }
}