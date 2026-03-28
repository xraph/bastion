"use client";

export function ThemedLogo() {
  return (
    <div className="relative flex items-center justify-center size-8">
      <svg
        viewBox="0 0 32 32"
        fill="none"
        xmlns="http://www.w3.org/2000/svg"
        className="size-8"
        aria-hidden="true"
      >
        {/* Shield/gateway symbol */}
        <rect
          x="2"
          y="2"
          width="28"
          height="28"
          rx="6"
          className="fill-blue-500 dark:fill-blue-400"
        />
        {/* Shield outline */}
        <path
          d="M16 6L8 10V16C8 20.4 11.4 24.5 16 26C20.6 24.5 24 20.4 24 16V10L16 6Z"
          className="fill-white/20"
        />
        {/* Arrow through shield (gateway) */}
        <path
          d="M11 16H21M18 13L21 16L18 19"
          stroke="white"
          strokeWidth="2"
          strokeLinecap="round"
          strokeLinejoin="round"
        />
      </svg>
    </div>
  );
}
