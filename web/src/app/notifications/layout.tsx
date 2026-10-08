import type { Metadata } from "next";

export const metadata: Metadata = {
  title: "Notifications",
  description: "New arrivals, upcoming releases and new episodes",
};

export default function NotificationsLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return children;
}