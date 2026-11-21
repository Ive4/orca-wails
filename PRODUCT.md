# Orca-Wails

<!-- impeccable:product-schema 1 -->

## Platform

web

The macOS menu bar and task panel use AppKit and SwiftUI. The Windows tray panel uses a Wails WebView.

## Product purpose

Orca-Wails keeps Orca Core available in the background. Users open the tray to check agent tasks, jump to the corresponding session, reach Bots, or manage the application and its core.

## Operating context

The primary tray panel is a small, transient window anchored to the system tray. The secondary click opens a system-native menu. Users can close the main Orca window while their tasks continue in the background.

## Capabilities and constraints

The tray shows active tasks and tasks completed in the last 24 hours. It presents the settings, update, and core operations available on the current platform. Operations that interrupt tasks have explicit confirmation. The panel uses the desktop's appearance setting and is usable with the keyboard.
