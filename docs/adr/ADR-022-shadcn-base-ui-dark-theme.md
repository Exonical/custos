# ADR-022: shadcn/ui on Base UI with a dark industrial theme

Status: Accepted  
Date: 2026-09-28

Custos adopts shadcn/ui `base-nova` on Base UI primitives rather than Radix, matching shadcn's current Base UI primitive library, and standardizes on a dark-only industrial theme with semantic OKLCH tokens, IBM Plex Sans/Mono, square corners, and a single signal-orange accent. The consequence is that there is no light mode or theme toggle for now; new UI must use the design tokens instead of raw palette classes.
