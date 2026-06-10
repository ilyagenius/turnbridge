import SwiftUI

struct AboutView: View {
    var body: some View {
        VStack(spacing: 20) {
            
            Image("AboutLogo")
                .resizable()
                .scaledToFit()
                .frame(width: 100, height: 100)
                .clipShape(RoundedRectangle(cornerRadius: 22, style: .continuous))
                .shadow(color: .black.opacity(0.1), radius: 10, x: 0, y: 5)
                .padding(.top, 40)
            
            Text("TurnBridge")
                .font(.largeTitle)
                .fontWeight(.bold)
            
            Text("Version \(Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String ?? "1.0")")
                .foregroundColor(.secondary)
            
            Divider()
                .padding(.horizontal, 40)
                .padding(.top, 10)
            
            VStack(spacing: 16) {
                Link(destination: URL(string: "https://github.com/nullcstring/turnbridge")!) {
                    HStack {
                        Image(systemName: "chevron.left.forwardslash.chevron.right")
                            .font(.system(size: 18, weight: .medium))
                            .frame(width: 30)
                        
                        Text("GitHub Repository")
                            .font(.system(size: 16, weight: .medium))
                        
                        Spacer()
                        
                        Image(systemName: "arrow.up.right")
                            .font(.system(size: 14, weight: .semibold))
                            .foregroundColor(.secondary)
                    }
                    .foregroundColor(.primary)
                    .padding()
                    .background(Color(UIColor.secondarySystemBackground))
                    .cornerRadius(12)
                }
                
                Link(destination: URL(string: "https://t.me/nullcstring")!) {
                    HStack {
                        Image(systemName: "paperplane.fill")
                            .font(.system(size: 18))
                            .frame(width: 30)
                        
                        Text("@nullcstring")
                            .font(.system(size: 16, weight: .medium))
                        
                        Spacer()
                        
                        Image(systemName: "arrow.up.right")
                            .font(.system(size: 14, weight: .semibold))
                            .foregroundColor(.blue.opacity(0.7))
                    }
                    .foregroundColor(.blue)
                    .padding()
                    .background(Color.blue.opacity(0.1))
                    .cornerRadius(12)
                }
            }
            .padding(.horizontal, 40)
            
            Spacer()
        }
        .navigationTitle("About")
        .navigationBarTitleDisplayMode(.inline)
    }
}
