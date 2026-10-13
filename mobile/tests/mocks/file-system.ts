/** Enough of expo-file-system for modules that import it to load. */
export class File {
  uri = 'file:///cache/test';
  exists = false;
  constructor(..._parts: unknown[]) {}
  create(): void {}
  delete(): void {}
  write(_content: string): void {}
}
export const Paths = { cache: 'file:///cache' };
