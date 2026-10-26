import type { Metadata } from "next";
import localFont from "next/font/local";
import "./globals.css";

const geistSans = localFont({
  src: "./fonts/Geist-Variable.woff2",
  variable: "--font-geist-sans",
  weight: "100 900",
  display: "swap",
});

const geistMono = localFont({
  src: "./fonts/GeistMono-Variable.woff2",
  variable: "--font-geist-mono",
  weight: "100 900",
  display: "swap",
});

const notoSerif = localFont({
  src: "./fonts/NotoSerifTC[wght].ttf",
  variable: "--font-noto-serif",
  weight: "500 600",
  display: "swap",
});

export const metadata: Metadata = {
  title: "LLM Wiki Cloud",
  description: "A searchable frontend for the LLM Wiki knowledge base.",
};

export const viewport = {
  themeColor: "#0b0b0c",
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="zh-Hant" className={`${geistSans.variable} ${geistMono.variable} ${notoSerif.variable} h-full antialiased`}>
      <body className="min-h-dvh font-sans">
        {children}
      </body>
    </html>
  );
}
