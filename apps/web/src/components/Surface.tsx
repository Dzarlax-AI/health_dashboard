import type { ElementType, ReactNode } from "react";

interface SurfaceProps {
  as?: ElementType;
  children: ReactNode;
  className?: string;
  tone?: "default" | "tinted" | "glass";
}

export function Surface({
  as: Component = "section",
  children,
  className = "",
  tone = "default",
}: SurfaceProps) {
  return (
    <Component className={`surface surface--${tone} ${className}`.trim()}>
      {children}
    </Component>
  );
}
