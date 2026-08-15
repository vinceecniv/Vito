-- Lay out the installer window of the mounted Vito disk image.
--
-- The layout is not a property of the image but of the volume's .DS_Store, so
-- the only way to set it is to have Finder actually open the window, arrange
-- it, and write the result out. build-macos.sh does that on a read-write image
-- and compresses it afterwards.
--
-- Usage:  osascript packaging/dmg-layout.applescript <volume name>

on run argv
	set volName to item 1 of argv

	tell application "Finder"
		tell disk volName
			open

			set current view of container window to icon view
			set toolbar visible of container window to false
			set statusbar visible of container window to false

			-- {left, top, right, bottom} of the whole window. The width matches
			-- the 640pt background exactly; the extra 28pt of height is the
			-- title bar, which sits outside the content area the picture fills.
			set the bounds of container window to {200, 140, 840, 568}

			set opts to the icon view options of container window
			set arrangement of opts to not arranged
			set icon size of opts to 128
			set text size of opts to 13
			set background picture of opts to file ".background:background.tiff"

			-- Positions are the centre of each icon, in the same coordinate
			-- space as the background art, so these two values are what the
			-- arrow in packaging/mkdmgbg points between.
			set position of item "Vito.app" of container window to {160, 200}
			set position of item "Applications" of container window to {480, 200}

			-- Force the layout to be written to the volume, then let go of it.
			update without registering applications
			delay 1
			close
		end tell
	end tell
end run
