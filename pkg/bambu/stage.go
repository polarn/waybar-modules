package bambu

import (
	"fmt"
	"strings"
)

// StageName renders a stg_cur id as human-readable text — the same
// status line the printer's own display shows ("heating hotend",
// "inspecting first layer", ...). IDs per ha-bambulab's
// CURRENT_STAGE_IDS table. Unknown ids render as "stage <n>".
func StageName(id int) string {
	if s, ok := stageNames[id]; ok {
		return strings.ReplaceAll(s, "_", " ")
	}
	return fmt.Sprintf("stage %d", id)
}

// StageIdle reports whether the id carries no information worth showing:
// idle markers (-1 X1-style, 255 P1-style) and 0 ("printing", redundant
// next to gcode_state RUNNING).
func StageIdle(id int) bool {
	return id == -1 || id == 255 || id == 0
}

var stageNames = map[int]string{
	0:  "printing",
	1:  "auto_bed_leveling",
	2:  "heatbed_preheating",
	3:  "sweeping_xy_mech_mode",
	4:  "changing_filament",
	5:  "m400_pause",
	6:  "paused_filament_runout",
	7:  "heating_hotend",
	8:  "calibrating_extrusion",
	9:  "scanning_bed_surface",
	10: "inspecting_first_layer",
	11: "identifying_build_plate_type",
	12: "calibrating_micro_lidar",
	13: "homing_toolhead",
	14: "cleaning_nozzle_tip",
	15: "checking_extruder_temperature",
	16: "paused_user",
	17: "paused_front_cover_falling",
	18: "calibrating_micro_lidar",
	19: "calibrating_extrusion_flow",
	20: "paused_nozzle_temperature_malfunction",
	21: "paused_heat_bed_temperature_malfunction",
	22: "filament_unloading",
	23: "paused_skipped_step",
	24: "filament_loading",
	25: "calibrating_motor_noise",
	26: "paused_ams_lost",
	27: "paused_low_fan_speed_heat_break",
	28: "paused_chamber_temperature_control_error",
	29: "cooling_chamber",
	30: "paused_user_gcode",
	31: "motor_noise_showoff",
	32: "paused_nozzle_filament_covered_detected",
	33: "paused_cutter_error",
	34: "paused_first_layer_error",
	35: "paused_nozzle_clog",
	36: "check_absolute_accuracy_before_calibration",
	37: "absolute_accuracy_calibration",
	38: "check_absolute_accuracy_after_calibration",
	39: "calibrate_nozzle_offset",
	40: "bed_level_high_temperature",
	41: "check_quick_release",
	42: "check_door_and_cover",
	43: "laser_calibration",
	44: "check_plaform",
	45: "check_birdeye_camera_position",
	46: "calibrate_birdeye_camera",
	47: "bed_level_phase_1",
	48: "bed_level_phase_2",
	49: "heating_chamber",
	50: "heated_bedcooling",
	51: "print_calibration_lines",
	52: "check_material",
	53: "calibrating_live_view_camera",
	54: "waiting_for_heatbed_temperature",
	55: "check_material_position",
	56: "calibrating_cutter_model_offset",
	57: "measuring_surface",
	58: "thermal_preconditioning",
	59: "homing_blade_holder",
	60: "calibrating_camera_offset",
	61: "calibrating_blade_holder_position",
	62: "hotend_pick_place_test",
	63: "waiting_chamber_temperature_equalize",
	64: "preparing_hotend",
	65: "calibrating_detection_nozzle_clumping",
	66: "purifying_chamber_air",
	67: "measuring_rotary_attachment",
	68: "moving_toolhead_above_purge_chute",
	69: "cooling_nozzle",
	70: "moving_toolhead_to_center_of_heatbed",
	71: "active_arc_fitting",
	72: "hotend_type_detection",
	73: "build_plate_alignment_detection",
	74: "heatbed_surface_foreign_object_detection",
	75: "heatbed_underside_foreign_object_detection",
	76: "pre_extrusion_before_printing",
	77: "preparing_ams",
	-1: "idle",
	255: "idle",
}
