import type { Metadata } from "next";
import { Geist, Geist_Mono } from "next/font/google";
import "./globals.css";

// Geist Sans + Geist Mono. The CSS variables are consumed by
// `app/globals.css` to drive --font-body and --font-display, which
// in turn power Tailwind's `font-body` / `font-display` utilities.
const geistSans = Geist({
  variable: "--font-geist-sans",
  subsets: ["latin"],
});

const geistMono = Geist_Mono({
  variable: "--font-geist-mono",
  subsets: ["latin"],
});

const SITE_TITLE = "agentcookie - session state sync for the agent on your second Mac";
const SITE_DESCRIPTION =
  "Cookies and per-CLI secrets, replicated continuously from your laptop to the Mac your agent runs on. Encrypted over Tailscale, zero per-site auth ceremony.";

export const metadata: Metadata = {
  title: SITE_TITLE,
  description: SITE_DESCRIPTION,
  metadataBase: new URL(
    process.env.NEXT_PUBLIC_PLATFORM_URL ?? "https://agentcookie.dev",
  ),
  openGraph: {
    title: SITE_TITLE,
    description: SITE_DESCRIPTION,
    url: "/",
    siteName: "agentcookie",
    type: "website",
  },
  twitter: {
    card: "summary_large_image",
    title: SITE_TITLE,
    description: SITE_DESCRIPTION,
  },
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="en">
      <body className={`${geistSans.variable} ${geistMono.variable}`}>
        {children}
      </body>
    </html>
  );
}
