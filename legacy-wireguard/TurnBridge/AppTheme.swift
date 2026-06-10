import SwiftUI

// MARK: - Theme Definition

enum AppTheme: String, CaseIterable {
    case system = "system"
    case dark   = "dark"
    case cyber  = "cyber"

    var displayName: String {
        switch self {
        case .system: return "System"
        case .dark:   return "Dark"
        case .cyber:  return "Cyber"
        }
    }

    var icon: String {
        switch self {
        case .system: return "circle.lefthalf.filled"
        case .dark:   return "moon.fill"
        case .cyber:  return "bolt.fill"
        }
    }

    var colorScheme: ColorScheme? {
        switch self {
        case .system: return nil
        case .dark, .cyber: return .dark
        }
    }

    var isCyber: Bool { self == .cyber }

    // Backgrounds
    var pageBackground: Color {
        switch self {
        case .system: return Color(.systemGroupedBackground)
        case .dark:   return Color(red: 0.08, green: 0.08, blue: 0.10)
        case .cyber:  return Color(red: 0.02, green: 0.02, blue: 0.08)
        }
    }

    var cardBackground: Color {
        switch self {
        case .system: return Color(.secondarySystemGroupedBackground)
        case .dark:   return Color(red: 0.12, green: 0.12, blue: 0.15)
        case .cyber:  return Color(red: 0.05, green: 0.05, blue: 0.14)
        }
    }

    // Accents
    var accent: Color {
        switch self {
        case .system, .dark: return .blue
        case .cyber:         return Color(red: 0.0, green: 0.95, blue: 1.0) // neon cyan
        }
    }

    var secondaryAccent: Color {
        switch self {
        case .system, .dark: return .purple
        case .cyber:         return Color(red: 0.65, green: 0.0, blue: 1.0) // neon purple
        }
    }

    var connectedColor: Color {
        switch self {
        case .system, .dark: return .green
        case .cyber:         return Color(red: 0.0, green: 1.0, blue: 0.55) // neon green
        }
    }

    // Glow radius for cyber
    var glowRadius: CGFloat { isCyber ? 14 : 0 }
    var cardBorderOpacity: Double { isCyber ? 0.6 : 0.15 }
    var cardBorderWidth: CGFloat { isCyber ? 1.0 : 0.5 }
}

// MARK: - Cyber Grid Background

struct CyberGrid: View {
    var body: some View {
        Canvas { ctx, size in
            let spacing: CGFloat = 32
            var path = Path()
            var x: CGFloat = 0
            while x <= size.width { path.move(to: .init(x: x, y: 0)); path.addLine(to: .init(x: x, y: size.height)); x += spacing }
            var y: CGFloat = 0
            while y <= size.height { path.move(to: .init(x: 0, y: y)); path.addLine(to: .init(x: size.width, y: y)); y += spacing }
            ctx.stroke(path, with: .color(Color(red: 0.0, green: 0.8, blue: 1.0).opacity(0.04)), lineWidth: 0.5)
        }
        .ignoresSafeArea()
    }
}

// MARK: - Theme Card Modifier

struct ThemedCard: ViewModifier {
    let theme: AppTheme
    var accent: Color? = nil
    var cornerRadius: CGFloat = 16

    func body(content: Content) -> some View {
        let accentColor = accent ?? theme.accent
        content
            .background(theme.cardBackground)
            .clipShape(RoundedRectangle(cornerRadius: cornerRadius, style: .continuous))
            .overlay(
                RoundedRectangle(cornerRadius: cornerRadius, style: .continuous)
                    .strokeBorder(
                        theme.isCyber
                            ? LinearGradient(colors: [accentColor.opacity(0.7), accentColor.opacity(0.15)],
                                             startPoint: .topLeading, endPoint: .bottomTrailing)
                            : LinearGradient(colors: [Color.primary.opacity(theme.cardBorderOpacity)],
                                             startPoint: .top, endPoint: .bottom),
                        lineWidth: theme.cardBorderWidth
                    )
            )
            .shadow(color: theme.isCyber ? accentColor.opacity(0.25) : .clear, radius: theme.glowRadius)
    }
}

extension View {
    func themedCard(_ theme: AppTheme, accent: Color? = nil, cornerRadius: CGFloat = 16) -> some View {
        modifier(ThemedCard(theme: theme, accent: accent, cornerRadius: cornerRadius))
    }
}
