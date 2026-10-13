import AsyncStorage from '@react-native-async-storage/async-storage';
import { createJSONStorage } from 'zustand/middleware';

/**
 * Where persisted stores keep their state on the device.
 *
 * The web client used localStorage; AsyncStorage is the same idea, except
 * asynchronous, so a persisted store renders its defaults for a frame and then
 * rehydrates. Nothing persisted here gates a decision the server makes.
 */
export const persistStorage = createJSONStorage(() => AsyncStorage);
