package clicker

// allowDialogClickScript is the AppleScript that locates Chrome's
// chrome://inspect "An app wants to debug this browser" Allow button and
// clicks it. The script tries several locator patterns in sequence because
// Chrome's permission UIs render through different Accessibility hierarchies
// across versions (window, sheet, floating bubble).
//
// Contract:
//   - On successful click: stdout begins with "OK:" followed by the
//     selector that matched.
//   - On "dialog not visible": stdout = "NO_DIALOG".
//   - On "Chrome not running": stdout = "NO_CHROME".
//   - Any other osascript error surfaces in stderr.
//
// IMPORTANT: the precise dialog locator should be verified empirically per
// docs/research/chrome-inspect-allow-dialog.md (v0.6 U1). Until U1 confirms
// against Matt's Mac mini Chrome 148, the strategies below are best-guess
// based on Chrome's typical permission-dialog rendering on macOS.
const allowDialogClickScript = `
tell application "System Events"
  if not (exists process "Google Chrome") then
    return "NO_CHROME"
  end if

  tell process "Google Chrome"
    -- Strategy 1: top-level window with the Allow button directly.
    try
      repeat with w in windows
        try
          if exists (button "Allow" of w) then
            click button "Allow" of w
            return "OK: window-direct"
          end if
        end try
      end repeat
    end try

    -- Strategy 2: sheet attached to a window.
    try
      repeat with w in windows
        try
          if exists (sheet 1 of w) then
            if exists (button "Allow" of sheet 1 of w) then
              click button "Allow" of sheet 1 of w
              return "OK: sheet"
            end if
          end if
        end try
      end repeat
    end try

    -- Strategy 3: floating window (the bubble Chrome uses for some prompts).
    try
      repeat with w in windows
        try
          if subrole of w is "AXDialog" or subrole of w is "AXSystemDialog" then
            if exists (button "Allow" of w) then
              click button "Allow" of w
              return "OK: ax-dialog"
            end if
          end if
        end try
      end repeat
    end try

    -- Strategy 4: search recursively through all UI elements of every
    -- window for a button whose name is "Allow". Slowest path; last resort.
    try
      repeat with w in windows
        try
          set foundButtons to (every button of w whose name is "Allow")
          if (count of foundButtons) > 0 then
            click (item 1 of foundButtons)
            return "OK: recursive"
          end if
        end try
      end repeat
    end try

    return "NO_DIALOG"
  end tell
end tell
`

// allowDialogVisibleScript is a non-clicking probe variant used in tests and
// dry-run debugging. Returns the locator that matched without clicking.
const allowDialogVisibleScript = `
tell application "System Events"
  if not (exists process "Google Chrome") then
    return "NO_CHROME"
  end if

  tell process "Google Chrome"
    try
      repeat with w in windows
        try
          if exists (button "Allow" of w) then
            return "VISIBLE: window-direct"
          end if
        end try
        try
          if exists (button "Allow" of sheet 1 of w) then
            return "VISIBLE: sheet"
          end if
        end try
        try
          set foundButtons to (every button of w whose name is "Allow")
          if (count of foundButtons) > 0 then
            return "VISIBLE: recursive"
          end if
        end try
      end repeat
    end try
    return "NO_DIALOG"
  end tell
end tell
`
