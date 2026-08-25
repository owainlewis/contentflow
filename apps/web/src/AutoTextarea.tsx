import { useLayoutEffect, useRef, type TextareaHTMLAttributes } from "react";

type Props = Omit<TextareaHTMLAttributes<HTMLTextAreaElement>, "rows"> & {
  value: string;
  /** Height floor, so an empty field still has presence. */
  minRows?: number;
};

// A textarea that grows to fit its content. Height is measured rather than
// estimated from character count, so wrapping, long words, and blank lines all
// come out right. A ResizeObserver re-measures when the column width changes,
// which happens when the sidebar collapses or the window is resized.
export default function AutoTextarea({ value, minRows = 2, className, ...props }: Props) {
  const ref = useRef<HTMLTextAreaElement>(null);

  useLayoutEffect(() => {
    const element = ref.current;
    if (!element) return;

    const resize = () => {
      const styles = window.getComputedStyle(element);
      const lineHeight = Number.parseFloat(styles.lineHeight) || 0;
      const vertical = Number.parseFloat(styles.paddingTop) + Number.parseFloat(styles.paddingBottom);
      // Collapse first so the content height can shrink as well as grow.
      element.style.height = "auto";
      const floor = lineHeight ? lineHeight * minRows + vertical : 0;
      element.style.height = `${Math.max(element.scrollHeight, floor)}px`;
    };

    resize();
    if (typeof ResizeObserver !== "function") return;
    const observer = new ResizeObserver(resize);
    observer.observe(element);
    return () => observer.disconnect();
  }, [value, minRows]);

  return <textarea ref={ref} value={value} rows={minRows} className={`auto-textarea ${className ?? ""}`.trim()} {...props} />;
}
