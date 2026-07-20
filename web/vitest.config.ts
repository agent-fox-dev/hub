import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    include: ["__tests__/**/*.test.ts"],
    testTimeout: 30_000,
    // Run files sequentially with a single fork to prevent destructive tests
    // (e.g., node_modules removal in E4/E7/E8) from crashing parallel workers.
    fileParallelism: false,
    pool: "forks",
    poolOptions: {
      forks: {
        singleFork: true,
      },
    },
  },
});
