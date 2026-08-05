#pragma once
#include "TrackerMotor.h"
#include "trackerDriver.h"
class TrackerMotor
{
public:
	static int maxAddress;
	int address = 0;
	int currentPosition = 0;
	int motorTemperature = 0;
	int controllerTemperature = 0;
	int alarmCode = 0;
	int homeStartingSpeed = 0;
	int homeSpeed = 1;
	int homeAcceleration = 1;
	int homePosition = 0;
	bool cw = true;
	int gearN = 1;
	int gearD = 1;
	int speed = 1;
	int acceleration = 1;
	int deceleration = 1;
	int wrapRange = 0;
	int wrapOffset = 0;
	bool realValues = false;

	bool readAllValues();
	bool writeAllValues();

	bool readCurrentPosition();
	bool writeHomePosition();




	TrackerMotor();
};

