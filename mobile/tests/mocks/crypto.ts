import { randomUUID as nodeRandomUUID } from 'node:crypto';

export const randomUUID = (): string => nodeRandomUUID();
